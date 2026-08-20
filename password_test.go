package main

// The routes that make you prove your password, and the promise underneath
// them: that the password itself never leaves the browser. See password.go.

import (
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// /auth/verify confirms the account password before the browser changes its
// local PIN lock. It must accept the real password, reject a wrong one, and be
// reachable only by a signed-in account.
func TestAuthVerify(t *testing.T) {
	ts, ck, _ := newTestServer(t)

	resp := doGet(t, ts, nil, "/auth/params?username=tester")
	var params struct {
		Salt string `json:"salt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&params); err != nil {
		t.Fatal(err)
	}
	good, _ := testDeriveKeys(t, "test-password", params.Salt)
	bad, _ := testDeriveKeys(t, "wrong-password", params.Salt)

	if r := doPost(t, ts, ck, "/auth/verify", url.Values{"password_auth": {good}}); r.StatusCode != 204 {
		t.Fatalf("correct password: got %d, want 204", r.StatusCode)
	}
	if r := doPost(t, ts, ck, "/auth/verify", url.Values{"password_auth": {bad}}); r.StatusCode != 403 {
		t.Fatalf("wrong password: got %d, want 403", r.StatusCode)
	}
	if r := doPost(t, ts, ck, "/auth/verify", url.Values{"password_auth": {""}}); r.StatusCode != 403 {
		t.Fatalf("empty password: got %d, want 403", r.StatusCode)
	}
	// a stranger with no valid session cannot use it to guess the password
	stranger := &http.Cookie{Name: sessionCookie, Value: "not-a-session"}
	if r := doPost(t, ts, stranger, "/auth/verify", url.Values{"password_auth": {good}}); r.StatusCode == 204 {
		t.Fatal("verify succeeded without a valid session")
	}
}

// 1x1 red PNG
var testPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
	0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0x50,
	0x0f, 0x00, 0x04, 0x85, 0x01, 0x80, 0x84, 0xa9, 0x8c, 0x21, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
	0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
}

// Changing the password is the one credential rotation the app has. It must
// demand the old password (a stolen session is not enough), install the new
// material atomically, and sign every other device out.
func TestPasswordChange(t *testing.T) {
	ts, ck, db := newTestServer(t)

	// the fixture account's auth key, re-derived the way the browser would
	var salt string
	db.QueryRow("SELECT kdf_salt FROM users WHERE id = 1").Scan(&salt)
	oldAuthKey, _ := testDeriveKeys(t, "test-password", salt)
	newForm, _, _ := testAccountKeys(t, "a whole new passphrase here")

	// a second session, standing in for another device
	otherToken := newSessionToken()
	db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, 1, ?)",
		hashToken(otherToken), time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05"))

	// wrong old password: refused, nothing changes
	resp := doPost(t, ts, ck, "/auth/password", url.Values{
		"old_auth_key":    {"bm90LXRoZS1yaWdodC1rZXktYXQtYWxsLXNvcnJ5ISE="},
		"new_auth_key":    newForm["auth_key"],
		"new_salt":        newForm["kdf_salt"],
		"new_wrapped_key": newForm["wrapped_key"],
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong password accepted: %d", resp.StatusCode)
	}

	// right old password: installed
	resp = doPost(t, ts, ck, "/auth/password", url.Values{
		"old_auth_key":    {oldAuthKey},
		"new_auth_key":    newForm["auth_key"],
		"new_salt":        newForm["kdf_salt"],
		"new_wrapped_key": newForm["wrapped_key"],
	})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("password change refused: %d %s", resp.StatusCode, bodyOf(t, resp))
	}

	var gotSalt, gotWrapped, gotHash string
	db.QueryRow("SELECT kdf_salt, wrapped_key, pass_hash FROM users WHERE id = 1").
		Scan(&gotSalt, &gotWrapped, &gotHash)
	if gotSalt != newForm.Get("kdf_salt") || gotWrapped != newForm.Get("wrapped_key") {
		t.Fatal("new key material was not stored")
	}
	if bcrypt.CompareHashAndPassword([]byte(gotHash), []byte(newForm.Get("auth_key"))) != nil {
		t.Fatal("the stored hash does not match the new auth key")
	}

	// this session survives; the other device's does not
	var sessions int
	db.QueryRow("SELECT count(*) FROM sessions WHERE user_id = 1").Scan(&sessions)
	if sessions != 1 {
		t.Fatalf("expected exactly the current session to survive, found %d", sessions)
	}
	if r := doGet(t, ts, ck, "/account"); r.StatusCode != http.StatusOK {
		t.Fatalf("the changing session was signed out: %d", r.StatusCode)
	}
}

// The password itself must never be able to reach the server, even by
// accident: the inputs that hold one have no name to submit under.
func TestPasswordsHaveNoNameToTravelUnder(t *testing.T) {
	for _, asset := range []string{"static/settings.js", "static/e2e.js"} {
		src := readAsset(t, asset)
		for _, line := range strings.Split(src, "\n") {
			if !strings.Contains(line, `type: "password"`) && !strings.Contains(line, `type="password"`) {
				continue
			}
			if strings.Contains(line, "name:") || strings.Contains(line, "name=") {
				t.Errorf("%s: a password input has a name and could be form-submitted: %s",
					asset, strings.TrimSpace(line))
			}
		}
	}
	// and the endpoints that prove a password only ever see derived keys
	js := readAsset(t, "static/settings.js")
	if !strings.Contains(js, "old_auth_key: old.authKey") {
		t.Error("the change flow no longer sends the derived auth key")
	}
	if !strings.Contains(js, "password_auth: derived.authKey") {
		t.Error("the delete flow no longer sends the derived auth key")
	}
	// The real invariant: no request body carries a password field's value.
	// A key *named* password_auth is fine — that is the derived proof — so
	// this reads what is being sent, not what it is called.
	for _, body := range regexp.MustCompile(`URLSearchParams\(\{[^}]*\}`).FindAllString(js, -1) {
		for _, held := range []string{"password.value", "current.value", "next.value", "pin"} {
			if strings.Contains(body, held) {
				t.Errorf("a request body carries %s: %s", held, body)
			}
		}
	}
}
