package main

// The three things you must prove your password to do.
//
// Changing it, deleting the account, and altering the PIN lock all share one
// shape: the browser derives the auth key from the password and the account's
// KDF salt and sends only that, exactly as signing in does, and the server
// checks it against the stored hash. The password itself never travels, so
// none of this can leak one — and every route here is behind the same login
// throttle, so none of them can become an oracle for guessing one either.
//
// Why prove it at all when the request already carries a session: because a
// found, unlocked laptop is a session. Without this, closing it would be the
// only thing standing between a stranger and an account with no password on
// it, no PIN on it, or no notes left in it.

import (
	"log"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const passFailPause = 400 * time.Millisecond

// authVerify confirms the account password for a signed-in user. Used before
// any change to the PIN lock; it stores nothing and reveals nothing beyond
// yes/no.
func (s *server) authVerify(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	key := throttleKey(r, u.Name)
	if blocked, wait := s.loginTries.blocked(key); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpError(w, http.StatusTooManyRequests,
			s.translatef("Too many attempts. Try again in %d minute(s).", int(wait.Minutes())+1))
		return
	}
	authKey := r.FormValue("password_auth")
	var passHash string
	if err := s.db.QueryRow("SELECT pass_hash FROM users WHERE id = ?", u.ID).Scan(&passHash); err != nil {
		httpError(w, http.StatusForbidden, s.translate("Wrong password."))
		return
	}
	if authKey == "" || bcrypt.CompareHashAndPassword([]byte(passHash), []byte(authKey)) != nil {
		s.loginTries.fail(key)
		time.Sleep(passFailPause)
		httpError(w, http.StatusForbidden, s.translate("Wrong password."))
		return
	}
	s.loginTries.reset(key)
	w.WriteHeader(http.StatusNoContent)
}

// changePassword rotates the whole credential set in one authenticated step.
//
// The browser does the cryptography: it proves the current password by
// deriving and sending the old auth key, and brings a fresh salt, a fresh
// auth key and the data key re-wrapped under the new password. The data key
// itself never changes and never appears here — so nothing has to be
// re-encrypted, and the server still cannot read anything afterwards.
//
// A signed-in session alone is deliberately not enough: sessions can be left
// open on other machines, and rotating the one credential that unlocks
// everything must cost knowledge of it.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u.ID == 0 {
		httpError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	oldAuthKey := r.FormValue("old_auth_key")
	newAuthKey := r.FormValue("new_auth_key")
	newSalt := r.FormValue("new_salt")
	newWrapped := r.FormValue("new_wrapped_key")
	if !validKeyMaterial(newAuthKey, newSalt, newWrapped) {
		httpError(w, http.StatusBadRequest, "malformed key material")
		return
	}

	// the old password is proven the same way login proves it, with the same
	// throttle, so this endpoint is no better an oracle than the login form
	key := throttleKey(r, u.Name)
	if blocked, wait := s.loginTries.blocked(key); blocked {
		httpError(w, http.StatusTooManyRequests,
			s.translatef("Too many attempts. Try again in %s.", wait))
		return
	}
	var passHash string
	if err := s.db.QueryRow("SELECT pass_hash FROM users WHERE id = ?", u.ID).Scan(&passHash); err != nil {
		httpError(w, 500, "could not load the account")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(passHash), []byte(oldAuthKey)) != nil {
		s.loginTries.fail(key)
		httpError(w, http.StatusForbidden, s.translate("The current password is wrong."))
		return
	}
	s.loginTries.reset(key)

	newHash, err := bcrypt.GenerateFromPassword([]byte(newAuthKey), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, 500, "could not hash the new key")
		return
	}
	if _, err := s.db.Exec(`UPDATE users SET pass_hash = ?, kdf_salt = ?, wrapped_key = ?
		WHERE id = ?`, string(newHash), newSalt, newWrapped, u.ID); err != nil {
		httpError(w, 500, "could not store the new credentials")
		return
	}

	// every other session dies with the old password; this one stays, because
	// the person holding it is the person who just proved the password
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.Exec("DELETE FROM sessions WHERE user_id = ? AND token_hash != ?",
			u.ID, hashToken(c.Value))
	} else {
		s.db.Exec("DELETE FROM sessions WHERE user_id = ?", u.ID)
	}
	log.Printf("password changed for %q (other sessions revoked)", u.Name)
	w.WriteHeader(http.StatusNoContent)
}

// deleteAccount erases the account and everything in it, for good.
//
// The password is proven first, exactly as signing in proves it: a session
// cookie says who you are, and someone who finds an unlocked laptop should not
// be able to destroy an account with one click. The row goes, and every record,
// blob, session, verification and preference goes with it through the schema's
// cascades — the server has nothing left that belonged to this account, and
// nothing readable to leave behind even if it did.
func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if u.ID == 0 {
		httpError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	key := throttleKey(r, u.Name)
	if blocked, wait := s.loginTries.blocked(key); blocked {
		httpError(w, http.StatusTooManyRequests,
			s.translatef("Too many attempts. Try again in %d minute(s).", int(wait.Minutes())+1))
		return
	}
	var passHash string
	if err := s.db.QueryRow("SELECT pass_hash FROM users WHERE id = ?", u.ID).Scan(&passHash); err != nil {
		httpError(w, http.StatusForbidden, s.translate("Wrong password."))
		return
	}
	authKey := r.FormValue("password_auth")
	if authKey == "" || bcrypt.CompareHashAndPassword([]byte(passHash), []byte(authKey)) != nil {
		s.loginTries.fail(key)
		time.Sleep(passFailPause)
		httpError(w, http.StatusForbidden, s.translate("Wrong password."))
		return
	}
	s.loginTries.reset(key)

	// Cascades depend on foreign keys being enforced on this connection; the
	// DSN turns them on, and this is the one place where a silent failure
	// would leave an account's records orphaned but readable-by-count forever.
	var fk int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		log.Printf("delete account %q: foreign keys are OFF; refusing to orphan records", u.Name)
		httpError(w, 500, "could not delete the account")
		return
	}
	if _, err := s.db.Exec("DELETE FROM users WHERE id = ?", u.ID); err != nil {
		log.Printf("delete account %q: %v", u.Name, err)
		httpError(w, 500, "could not delete the account")
		return
	}
	log.Printf("account %q (id %d) deleted at its own request from %s", u.Name, u.ID, clientIP(r))

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	w.WriteHeader(http.StatusNoContent)
}
