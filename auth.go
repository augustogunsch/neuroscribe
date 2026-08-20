package main

// Authentication: bcrypt-hashed users, random session tokens (stored hashed),
// HttpOnly SameSite=Lax cookies.
//
// Accounts are created through the public sign-up form and nowhere else, and
// there is deliberately no account-management CLI: with end-to-end encryption
// the server cannot rotate a password (only the browser holds the key that
// unwraps the data key), so the one thing such a CLI would most look like it
// could do, it could not. Everything else — listing accounts, verifying an
// address, setting a plan, deleting a user — is a single SQL statement against
// the database file, available to anyone with the shell access a CLI would
// have required anyway.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "ng_session"
	sessionTTL    = 30 * 24 * time.Hour
	// must match NG_KDF_ITERATIONS in static/crypto.js
	kdfIterations = 600000
)

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newSessionToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

// sessionUser is the account behind a request, carried in the request context
// so handlers can build a scoped store without asking the database again.
type sessionUser struct {
	ID   int64
	Name string
}

type userCtxKeyType struct{}

var userCtxKey userCtxKeyType

func withUser(r *http.Request, u sessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userCtxKey, u))
}

func userFrom(r *http.Request) sessionUser {
	u, _ := r.Context().Value(userCtxKey).(sessionUser)
	return u
}

// lookupSession resolves the session cookie against the database.
func (s *server) lookupSession(r *http.Request) (sessionUser, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return sessionUser{}, false
	}
	var u sessionUser
	err = s.db.QueryRow(`SELECT u.id, u.username FROM sessions se JOIN users u ON u.id = se.user_id
		WHERE se.token_hash = ? AND se.expires_at > datetime('now')`, hashToken(c.Value)).Scan(&u.ID, &u.Name)
	if err != nil {
		return sessionUser{}, false
	}
	return u, true
}

// currentUser returns the signed-in username, "" when there is none.
func (s *server) currentUser(r *http.Request) string {
	if u := userFrom(r); u.ID != 0 {
		return u.Name
	}
	u, ok := s.lookupSession(r)
	if !ok {
		return ""
	}
	return u.Name
}

// requireAuth wraps the whole mux; public paths are the landing, login and
// sign-up pages plus static assets. On success the account travels onward in
// the request context, which is what binds every query to one owner.
func (s *server) requireAuth(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	u, signedIn := s.lookupSession(r)
	if signedIn {
		r = withUser(r, u)
	}
	p := r.URL.Path
	switch {
	case p == "/", p == "/healthz", p == "/login", p == "/auth/params",
		p == "/register", p == "/register/resend",
		p == "/verify", p == "/altcha/challenge",
		strings.HasPrefix(p, "/static/"),
		// the installable-app plumbing: none of it carries user data, and the
		// worker has to be fetchable before there is a session to fetch it with
		p == "/sw.js", p == "/manifest.webmanifest",
		strings.HasPrefix(p, "/strings/"),
		// fetched by the sandboxed runner, which has no session to send
		strings.HasPrefix(p, "/pyodide/"),
		// the Android app, offered from the landing page to people who by
		// definition have not signed in yet — and which carries no account
		// data, being the same file for everyone who asks
		p == "/download/"+apkName:
		return r, true
	}
	if signedIn {
		return r, true
	}
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", "/login")
		w.WriteHeader(http.StatusUnauthorized)
		return r, false
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
	return r, false
}

type loginPage struct {
	Error        string
	Flash        string
	NoUsers      bool
	CSRF         string
	Registration bool // public sign-up available
	ShowResend   bool // offer to resend a verification link
}

func (s *server) hasUsers() bool {
	var n int
	_ = s.db.QueryRow("SELECT count(*) FROM users").Scan(&n)
	return n > 0
}

func (s *server) showLogin(w http.ResponseWriter, r *http.Request) {
	// Deliberately shown to signed-in users too. A session cookie proves who
	// you are; it does not decrypt anything — a fresh tab has no data key, and
	// re-entering the password here is how it gets one. Bouncing signed-in
	// visitors to "/" would trap a keyless tab in a redirect loop.
	s.renderPage(w, r, "login.html", loginPage{
		NoUsers:      !s.hasUsers(),
		Registration: s.registrationOpen(),
		CSRF:         csrfFromContext(r),
		Flash:        takeFlash(w, r),
	})
}

// identifierClause matches an account by username or, when the identifier
// carries an "@", by email — usernames cannot contain one, so the two spaces
// never collide and nothing has to guess.
func identifierClause(identifier string) string {
	if strings.Contains(identifier, "@") {
		return "email = ? COLLATE NOCASE"
	}
	return "username = ? COLLATE NOCASE"
}

func (s *server) doLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	// the browser sends a key derived from the password, never the password
	authKey := r.FormValue("auth_key")

	// throttle brute force per (IP, username)
	key := throttleKey(r, username)
	if blocked, wait := s.loginTries.blocked(key); blocked {
		w.WriteHeader(http.StatusTooManyRequests)
		s.renderPage(w, r, "login.html", loginPage{
			Error:        s.translatef("Too many attempts. Try again in %d minute(s).", int(wait.Minutes())+1),
			NoUsers:      !s.hasUsers(),
			Registration: s.registrationOpen(),
			CSRF:         csrfFromContext(r),
		})
		return
	}

	var userID int64
	var passHash, wrappedKey string
	var verified int
	err := s.db.QueryRow(
		"SELECT id, pass_hash, email_verified, wrapped_key FROM users WHERE "+identifierClause(username),
		username).Scan(&userID, &passHash, &verified, &wrappedKey)
	if err != nil {
		// burn the same time as a real bcrypt check to avoid user enumeration
		bcrypt.CompareHashAndPassword([]byte("$2a$10$0123456789012345678901uvGnwbXVbXBYkPPQAJXBGJ5c1jrKoW6"), []byte(authKey))
		err = bcrypt.ErrMismatchedHashAndPassword
	} else {
		err = bcrypt.CompareHashAndPassword([]byte(passHash), []byte(authKey))
	}
	if err != nil {
		s.loginTries.fail(key)
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusUnauthorized)
		s.renderPage(w, r, "login.html", loginPage{
			Error:        s.translate("Wrong username or password."),
			NoUsers:      !s.hasUsers(),
			Registration: s.registrationOpen(),
			CSRF:         csrfFromContext(r),
		})
		return
	}
	var kdfSalt string
	s.db.QueryRow("SELECT kdf_salt FROM users WHERE id = ?", userID).Scan(&kdfSalt)
	if kdfSalt == "" {
		// created before end-to-end encryption: there is no key to unlock with
		w.WriteHeader(http.StatusForbidden)
		s.renderPage(w, r, "login.html", loginPage{
			Error:        s.translate("This account is missing its encryption keys and cannot be used. Please sign up again."),
			Registration: s.registrationOpen(),
			CSRF:         csrfFromContext(r),
		})
		return
	}
	if verified == 0 {
		// correct password, but the address was never confirmed
		w.WriteHeader(http.StatusForbidden)
		s.renderPage(w, r, "login.html", loginPage{
			Error:        s.translate("Confirm your email address before signing in."),
			ShowResend:   true,
			Registration: s.registrationOpen(),
			CSRF:         csrfFromContext(r),
		})
		return
	}
	s.loginTries.reset(key)
	log.Printf("login: %q signed in from %s", username, clientIP(r))
	token := newSessionToken()
	s.db.Exec("DELETE FROM sessions WHERE expires_at <= datetime('now')")
	_, dberr := s.db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hashToken(token), userID, time.Now().UTC().Add(sessionTTL).Format("2006-01-02 15:04:05"))
	if dberr != nil {
		// the classic cause is a database the service user cannot write —
		// root-owned WAL files from a tool run as root; say so in the journal
		log.Printf("create session for %q: %v", username, dberr)
		httpError(w, 500, "failed to create session")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	// The browser needs the wrapped data key to unlock. It is useless without
	// the password, so handing it back here tells the server nothing it could
	// use to read notes.
	if r.Header.Get("X-Requested-With") == "neuroscribe" {
		writeJSONHeader(w)
		writeJSON(w, map[string]string{"wrapped_key": wrappedKey, "redirect": "/"})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// authParams hands out the KDF salt for a username so the browser can derive
// its keys. Unknown names get a stable decoy salt derived from a server
// secret, so this cannot be used to test which accounts exist.
func (s *server) authParams(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	var salt string
	err := s.db.QueryRow("SELECT kdf_salt FROM users WHERE "+identifierClause(username), username).Scan(&salt)
	if err != nil || salt == "" {
		mac := hmac.New(sha256.New, s.altcha.key)
		mac.Write([]byte("decoy-salt:" + strings.ToLower(username)))
		salt = base64.StdEncoding.EncodeToString(mac.Sum(nil)[:16])
	}
	writeJSONHeader(w)
	writeJSON(w, map[string]any{"salt": salt, "iterations": kdfIterations})
}

func (s *server) doLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.Exec("DELETE FROM sessions WHERE token_hash = ?", hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
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
