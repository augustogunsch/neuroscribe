package app

// Public sign-up: username + email + password, gated by an Altcha proof of
// work, activated by clicking a link mailed to the address. Accounts stay
// inactive (and cannot log in) until verified, and unverified ones are swept
// away after a week so they do not squat usernames.

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	verifyTTL         = 24 * time.Hour
	unverifiedMaxAge  = 7 * 24 * time.Hour
	minPasswordLen    = 8
	maxPasswordBytes  = 72 // bcrypt ignores anything past this
	registerPerWindow = 5
	registerWindow    = time.Hour
)

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{2,31}$`)

// validKeyMaterial sanity-checks what the browser derived. The server cannot
// verify the password behind it — that is the point — but it can insist the
// pieces are the right shape before storing them.
func validKeyMaterial(authKey, salt, wrapped string) bool {
	auth, err1 := base64.StdEncoding.DecodeString(authKey)
	saltBytes, err2 := base64.StdEncoding.DecodeString(salt)
	if err1 != nil || err2 != nil || len(auth) != 32 || len(saltBytes) < 16 {
		return false
	}
	parts := strings.Split(wrapped, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		return false
	}
	iv, err1 := base64.StdEncoding.DecodeString(parts[1])
	ct, err2 := base64.StdEncoding.DecodeString(parts[2])
	return err1 == nil && err2 == nil && len(iv) == 12 && len(ct) >= 48
}

type registerPage struct {
	Error    string
	Notice   string
	Username string
	Email    string
	CSRF     string
	Done     bool
}

// registrationOpen requires both a working mailer and an explicit opt-in:
// sign-up without verification mail would strand every account, and running
// an open instance should be a deliberate act rather than a side effect of
// configuring SMTP.
func (s *server) registrationOpen() bool {
	return s.mail.configured() && s.registrationFlag
}

// purgeUnverified drops sign-ups that were never confirmed.
func purgeUnverified(db *sql.DB) {
	db.Exec(`DELETE FROM users WHERE email_verified = 0
		AND created_at < datetime('now', ?)`, fmt.Sprintf("-%d days", int(unverifiedMaxAge.Hours()/24)))
	db.Exec("DELETE FROM verifications WHERE expires_at <= datetime('now')")
}

func (s *server) showRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registrationOpen() {
		httpError(w, http.StatusNotFound, "registration is closed on this server")
		return
	}
	if s.currentUser(r) != "" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderPage(w, r, "register.html", registerPage{CSRF: csrfFromContext(r)})
}

func (s *server) renderRegisterError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.WriteHeader(code)
	s.renderPage(w, r, "register.html", registerPage{
		Error:    msg,
		Username: strings.TrimSpace(r.FormValue("username")),
		Email:    strings.TrimSpace(r.FormValue("email")),
		CSRF:     csrfFromContext(r),
	})
}

func (s *server) doRegister(w http.ResponseWriter, r *http.Request) {
	if !s.registrationOpen() {
		httpError(w, http.StatusNotFound, "registration is closed on this server")
		return
	}
	key := throttleKey(r, "register")
	if blocked, wait := s.registerTries.blocked(key); blocked {
		s.renderRegisterError(w, r, http.StatusTooManyRequests,
			s.translatef("Too many attempts. Try again in %d minute(s).", int(wait.Minutes())+1))
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	email := strings.TrimSpace(r.FormValue("email"))
	// The password itself never arrives here. The browser sends what it
	// derived: a key that only proves knowledge of the password, plus the
	// account key sealed with a key we never receive.
	authKey := r.FormValue("auth_key")
	kdfSalt := r.FormValue("kdf_salt")
	wrappedKey := r.FormValue("wrapped_key")

	if err := s.altcha.verify(r.FormValue("altcha")); err != nil {
		log.Printf("register: captcha rejected for %q from %s", username, clientIP(r))
		s.registerTries.fail(key)
		s.renderRegisterError(w, r, http.StatusBadRequest, s.translate("captcha check failed, please try again"))
		return
	}
	if !usernameRe.MatchString(username) {
		s.renderRegisterError(w, r, http.StatusBadRequest,
			s.translate("Username must be 3-32 characters: letters, digits, dot, dash or underscore."))
		return
	}
	if !validEmail(email) {
		s.renderRegisterError(w, r, http.StatusBadRequest, s.translate("That email address does not look valid."))
		return
	}
	if !validKeyMaterial(authKey, kdfSalt, wrappedKey) {
		s.renderRegisterError(w, r, http.StatusBadRequest,
			s.translate("Your browser could not prepare the encryption keys. Enable JavaScript and try again."))
		return
	}
	var taken int
	s.db.QueryRow("SELECT count(*) FROM users WHERE username = ? COLLATE NOCASE", username).Scan(&taken)
	if taken > 0 {
		s.renderRegisterError(w, r, http.StatusConflict, s.translate("That username is already taken."))
		return
	}
	s.registerTries.fail(key) // every genuine attempt counts toward the window

	// From here on the answer is always the same page, so a stranger cannot
	// probe which addresses already have accounts.
	done := registerPage{Done: true, Email: email, CSRF: csrfFromContext(r),
		Notice: s.translate("Check your inbox, we sent you a link to confirm your address.")}

	var emailTaken int
	s.db.QueryRow("SELECT count(*) FROM users WHERE email = ? COLLATE NOCASE", email).Scan(&emailTaken)
	if emailTaken > 0 {
		// the visitor sees the neutral "check your inbox" page either way
		log.Printf("register: %s already has an account, no mail sent", email)
		s.renderPage(w, r, "register.html", done)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(authKey), bcrypt.DefaultCost)
	if err != nil {
		httpError(w, 500, "failed to create account")
		return
	}
	res, err := s.db.Exec(`INSERT INTO users (username, pass_hash, kdf_salt, wrapped_key, email, email_verified)
		VALUES (?, ?, ?, ?, ?, 0)`, username, string(hash), kdfSalt, wrappedKey, email)
	if err != nil {
		s.renderRegisterError(w, r, http.StatusConflict, s.translate("That username is already taken."))
		return
	}
	userID, _ := res.LastInsertId()
	log.Printf("register: created account %q <%s> (id %d) from %s — awaiting confirmation",
		username, email, userID, clientIP(r))
	if err := s.sendVerification(userID, username, email); err != nil {
		log.Printf("register: rolling back account %q — verification mail failed: %v", username, err)
		s.db.Exec("DELETE FROM users WHERE id = ?", userID)
		s.renderRegisterError(w, r, http.StatusBadGateway,
			s.translate("We could not send the confirmation email. Please try again later."))
		return
	}
	s.renderPage(w, r, "register.html", done)
}

// sendVerification issues a fresh token and mails the activation link.
func (s *server) sendVerification(userID int64, username, email string) error {
	token := newSessionToken()
	s.db.Exec("DELETE FROM verifications WHERE user_id = ? AND purpose = 'verify'", userID)
	if _, err := s.db.Exec(
		"INSERT INTO verifications (token_hash, user_id, purpose, expires_at) VALUES (?, ?, 'verify', ?)",
		hashToken(token), userID, time.Now().UTC().Add(verifyTTL).Format("2006-01-02 15:04:05")); err != nil {
		return err
	}
	return s.mail.sendVerification(email, username, token)
}

func (s *server) verifyEmail(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	var userID int64
	err := s.db.QueryRow(`SELECT user_id FROM verifications
		WHERE token_hash = ? AND purpose = 'verify' AND expires_at > datetime('now')`,
		hashToken(token)).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		s.renderPage(w, r, "register.html", registerPage{
			Done:  true,
			CSRF:  csrfFromContext(r),
			Error: s.translate("This confirmation link is invalid or has expired. Sign up again to get a new one."),
		})
		return
	} else if err != nil {
		httpError(w, 500, "verification failed")
		return
	}
	s.db.Exec("UPDATE users SET email_verified = 1 WHERE id = ?", userID)
	s.db.Exec("DELETE FROM verifications WHERE user_id = ? AND purpose = 'verify'", userID)
	var confirmed string
	s.db.QueryRow("SELECT username FROM users WHERE id = ?", userID).Scan(&confirmed)
	log.Printf("register: account %q (id %d) confirmed from %s", confirmed, userID, clientIP(r))
	s.flashError(w, r, "/login", s.translate("Address confirmed, you can sign in now."))
}

// resendVerification re-sends the link for an address that is still pending.
func (s *server) resendVerification(w http.ResponseWriter, r *http.Request) {
	if !s.registrationOpen() {
		httpError(w, http.StatusNotFound, "registration is closed on this server")
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	key := throttleKey(r, "resend")
	if blocked, _ := s.registerTries.blocked(key); blocked {
		s.renderPage(w, r, "register.html", registerPage{
			Done: true, CSRF: csrfFromContext(r),
			Notice: s.translate("Check your inbox, we sent you a link to confirm your address."),
		})
		return
	}
	s.registerTries.fail(key)

	var userID int64
	var username string
	err := s.db.QueryRow("SELECT id, username FROM users WHERE email = ? COLLATE NOCASE AND email_verified = 0",
		email).Scan(&userID, &username)
	if err == nil {
		log.Printf("register: resending confirmation to %s (%q) for %s", email, username, clientIP(r))
		if serr := s.sendVerification(userID, username, email); serr != nil {
			log.Printf("register: resend to %s failed: %v", email, serr)
		}
	} else {
		log.Printf("register: resend requested from %s for an address with nothing pending", clientIP(r))
	}
	s.renderPage(w, r, "register.html", registerPage{
		Done: true, CSRF: csrfFromContext(r),
		Notice: s.translate("Check your inbox, we sent you a link to confirm your address."),
	})
}

// altchaChallengeHandler feeds the widget a fresh challenge.
func (s *server) altchaChallengeHandler(w http.ResponseWriter, r *http.Request) {
	c, err := s.altcha.newChallenge()
	if err != nil {
		httpError(w, 500, "failed to build challenge")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, c)
}
