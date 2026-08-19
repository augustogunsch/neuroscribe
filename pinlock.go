package main

// Password confirmation for PIN management.
//
// The PIN lock itself is entirely in the browser (static/lock.js): the sealed
// key lives in local storage so that unlocking works with no network at all,
// which is what an offline-first client needs. The cost of that choice is
// stated plainly where the PIN is set — a six-digit secret guarding local
// ciphertext is only a speed bump against someone who copies the whole profile.
//
// The one thing that cannot be decided on the client is whether the person
// setting, changing or removing the PIN actually knows the account password.
// Proving that stops someone who finds an unlocked session from quietly swapping
// or dropping the lock. The browser proves it the same way login does — it
// derives the auth key from the password and the account's KDF salt and sends
// only that — and this endpoint checks it against the stored hash, throttled so
// it cannot become a password-guessing oracle.

import (
	"net/http"
	"strconv"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const passFailPause = 400 * time.Millisecond

// authVerify confirms the account password for a signed-in user. It is used
// before any change to the PIN lock; it stores nothing and reveals nothing
// beyond yes/no.
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
