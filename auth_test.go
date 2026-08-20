package main

// Signing in, and staying signed in.

import (
	"encoding/json"
	"net/http"
	"net/smtp"
	"net/url"
	"testing"
)

func TestAuthRequired(t *testing.T) {
	ts, _, _ := newTestServer(t)
	for _, path := range []string{"/settings", "/trash", "/types", "/notes/welcome-to-neuroscribe"} {
		resp := doGet(t, ts, nil, path)
		if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login" {
			t.Fatalf("%s: expected redirect to /login, got %d %q",
				path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
}

func TestLoginFlow(t *testing.T) {
	ts, _, _ := newTestServer(t)
	if resp := signIn(t, ts, "tester", "wrong-password"); resp.StatusCode != 401 {
		t.Fatalf("wrong password: got %d", resp.StatusCode)
	}
	resp := signIn(t, ts, "tester", "test-password")
	if resp.StatusCode != 303 {
		t.Fatalf("login: got %d", resp.StatusCode)
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie && c.Value != "" && c.HttpOnly {
			found = true
		}
	}
	if !found {
		t.Fatal("no session cookie set")
	}
}

func TestLoginThrottle(t *testing.T) {
	ts, _, _ := newTestServer(t)
	for i := 0; i < maxLoginFailures; i++ {
		signIn(t, ts, "tester", "wrong-password")
	}
	// even the right password is refused while locked out
	resp := signIn(t, ts, "tester", "test-password")
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("throttle not engaged: %d", resp.StatusCode)
	}
}

// Uniqueness has to hold in the schema, not just in the sign-up handler: the
// login lookup is case-insensitive, so two accounts differing only in case
// would both answer to one identifier.
func TestUsernamesAreUniqueRegardlessOfCase(t *testing.T) {
	_, _, db := newTestServer(t)
	_, err := db.Exec(`INSERT INTO users (username, pass_hash, email_verified)
		VALUES ('TESTER', 'x', 1)`)
	if err == nil {
		t.Fatal("a second account differing only in username case was accepted")
	}
}

// The email is as much "who am I" as the username; both must open the same
// account, and asking for an unknown email must be indistinguishable from a
// known one (the decoy salt), or sign-in doubles as an enumeration oracle.
func TestLoginByEmail(t *testing.T) {
	ts, ck, _ := newTestServer(t)

	var salt string
	// /auth/params answers for the email exactly as for the username
	resp := doGet(t, ts, nil, "/auth/params?username=tester%40example.com")
	var params struct {
		Salt string `json:"salt"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, resp)), &params); err != nil || params.Salt == "" {
		t.Fatalf("no salt for the email identifier: %v", err)
	}
	salt = params.Salt

	authKey, _ := testDeriveKeys(t, "test-password", salt)
	login := doPost(t, ts, ck, "/login", url.Values{
		"username": {"tester@example.com"},
		"auth_key": {authKey},
	})
	if login.StatusCode != http.StatusSeeOther && login.StatusCode != http.StatusOK {
		t.Fatalf("email login refused: %d %s", login.StatusCode, bodyOf(t, login))
	}

	// unknown addresses get a decoy salt, never an error
	unknown := doGet(t, ts, nil, "/auth/params?username=nobody%40example.com")
	if err := json.Unmarshal([]byte(bodyOf(t, unknown)), &params); err != nil || params.Salt == "" {
		t.Fatal("an unknown email is distinguishable at /auth/params")
	}
}

func TestLoginAuthExchange(t *testing.T) {
	a := &loginAuth{user: "someone@example.com", pass: "token", host: "smtp.example.com"}
	// credentials must never go out in the clear to a remote server
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: false}); err == nil {
		t.Fatal("LOGIN accepted an unencrypted connection")
	}
	proto, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.example.com", TLS: true})
	if err != nil || proto != "LOGIN" {
		t.Fatalf("Start over TLS: %q %v", proto, err)
	}
	user, err := a.Next([]byte("Username:"), true)
	if err != nil || string(user) != "someone@example.com" {
		t.Fatalf("username step: %q %v", user, err)
	}
	pass, err := a.Next([]byte("Password:"), true)
	if err != nil || string(pass) != "token" {
		t.Fatalf("password step: %q %v", pass, err)
	}
	if _, err := a.Next([]byte("Surprise:"), true); err == nil {
		t.Fatal("unknown challenge accepted")
	}
}
