package app

// Public sign-up: the proof of work, the mail, and the ways it has to fail.

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func TestRegistrationFlow(t *testing.T) {
	ts, _, db, srv := newTestServerFull(t)
	sent := withMailCapture(t, ts, srv)

	form, _, _ := testAccountKeys(t, "a-good-password")
	form.Set("username", "newbie")
	form.Set("email", "newbie@example.com")
	form.Set("altcha", solveAltcha(t, ts))
	resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", form)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "Check your inbox") {
		t.Fatalf("register: %d\n%s", resp.StatusCode, body)
	}
	var verified int
	if err := db.QueryRow("SELECT email_verified FROM users WHERE username = 'newbie'").Scan(&verified); err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if verified != 0 {
		t.Fatal("new account should start unverified")
	}
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "newbie@example.com") {
		t.Fatalf("verification mail not sent: %v", *sent)
	}

	// unverified accounts cannot sign in even with the right password
	if resp := signIn(t, ts, "newbie", "a-good-password"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unverified login: %d", resp.StatusCode)
	}

	// follow the emailed link
	link := regexp.MustCompile(`/verify\?token=[a-f0-9]+`).FindString((*sent)[0])
	if link == "" {
		t.Fatalf("no verification link in mail: %s", (*sent)[0])
	}
	if resp = doGet(t, ts, nil, link); resp.StatusCode != 303 {
		t.Fatalf("verify: %d", resp.StatusCode)
	}
	db.QueryRow("SELECT email_verified FROM users WHERE username = 'newbie'").Scan(&verified)
	if verified != 1 {
		t.Fatal("verification did not stick")
	}
	// the same token cannot be replayed
	if resp = doGet(t, ts, nil, link); resp.StatusCode == 303 {
		t.Fatal("verification token reusable")
	}
	// and now the account works
	if resp := signIn(t, ts, "newbie", "a-good-password"); resp.StatusCode != 303 {
		t.Fatalf("verified login: %d", resp.StatusCode)
	}
}

func TestRegistrationRequiresCaptcha(t *testing.T) {
	ts, _, db, srv := newTestServerFull(t)
	withMailCapture(t, ts, srv)

	for _, altcha := range []string{"", "bogus", base64.StdEncoding.EncodeToString([]byte(`{"algorithm":"SHA-256","challenge":"x","number":1,"salt":"y","signature":"z"}`))} {
		resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", url.Values{
			"username": {"spammer"}, "email": {"spam@example.com"},
			"altcha": {altcha},
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("captcha %q accepted: %d", altcha, resp.StatusCode)
		}
	}
	var n int
	db.QueryRow("SELECT count(*) FROM users WHERE username = 'spammer'").Scan(&n)
	if n != 0 {
		t.Fatal("account created without a valid captcha")
	}
}

func TestAltchaSolutionIsSingleUse(t *testing.T) {
	ts, _, _, srv := newTestServerFull(t)
	withMailCapture(t, ts, srv)
	payload := solveAltcha(t, ts)

	first, _, _ := testAccountKeys(t, "a-good-password")
	first.Set("username", "first")
	first.Set("email", "first@example.com")
	first.Set("altcha", payload)
	if resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", first); resp.StatusCode != 200 {
		t.Fatalf("first use: %d", resp.StatusCode)
	}
	second, _, _ := testAccountKeys(t, "a-good-password")
	second.Set("username", "second")
	second.Set("email", "second@example.com")
	second.Set("altcha", payload)
	if resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", second); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed captcha accepted: %d", resp.StatusCode)
	}
}

func TestRegistrationDoesNotLeakEmails(t *testing.T) {
	ts, _, db, srv := newTestServerFull(t)
	sent := withMailCapture(t, ts, srv)
	db.Exec("INSERT INTO users (username, pass_hash, email, email_verified) VALUES ('taken', 'x', 'taken@example.com', 1)")

	form, _, _ := testAccountKeys(t, "a-good-password")
	form.Set("username", "someoneelse")
	form.Set("email", "taken@example.com")
	form.Set("altcha", solveAltcha(t, ts))
	resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", form)
	body := bodyOf(t, resp)
	if resp.StatusCode != 200 || !strings.Contains(body, "Check your inbox") {
		t.Fatalf("existing address should look identical: %d\n%s", resp.StatusCode, body)
	}
	if len(*sent) != 0 {
		t.Fatalf("mail sent for an address that already has an account: %v", *sent)
	}
	var n int
	db.QueryRow("SELECT count(*) FROM users WHERE username = 'someoneelse'").Scan(&n)
	if n != 0 {
		t.Fatal("duplicate-email account was created")
	}
}

func TestRegistrationClosedWithoutMail(t *testing.T) {
	ts, ck, _, _ := newTestServerFull(t) // no mailer configured
	if resp := doGet(t, ts, ck, "/register"); resp.StatusCode != 404 {
		t.Fatalf("registration should be closed without SMTP: %d", resp.StatusCode)
	}
}

func TestRegistrationRejectsBadKeyMaterial(t *testing.T) {
	ts, _, db, srv := newTestServerFull(t)
	withMailCapture(t, ts, srv)

	// the server never sees a password, so all it can do is insist the
	// browser's key material is well formed
	for _, broken := range []func(url.Values){
		func(f url.Values) { f.Set("auth_key", "not-base64!") },
		func(f url.Values) { f.Set("auth_key", base64.StdEncoding.EncodeToString([]byte("too short"))) },
		func(f url.Values) { f.Set("kdf_salt", "") },
		func(f url.Values) { f.Set("wrapped_key", "v1.nope") },
		func(f url.Values) { f.Set("wrapped_key", "") },
	} {
		form, _, _ := testAccountKeys(t, "a-good-password")
		form.Set("username", "broken")
		form.Set("email", "broken@example.com")
		form.Set("altcha", solveAltcha(t, ts))
		broken(form)
		if resp := doPost(t, ts, &http.Cookie{Name: "x"}, "/register", form); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("malformed key material accepted: %d", resp.StatusCode)
		}
	}
	var n int
	db.QueryRow("SELECT count(*) FROM users WHERE username = 'broken'").Scan(&n)
	if n != 0 {
		t.Fatal("account created from malformed key material")
	}
}

func TestValidEmail(t *testing.T) {
	for _, ok := range []string{"a@b.co", "augusto@augustogunsch.com", "x.y+z@mail.example.org"} {
		if !validEmail(ok) {
			t.Errorf("rejected valid address %q", ok)
		}
	}
	for _, bad := range []string{"", "nope", "a@b", "a@b.co\r\nBcc: evil@x.com", "<a@b.co>", strings.Repeat("a", 250) + "@b.co"} {
		if validEmail(bad) {
			t.Errorf("accepted invalid address %q", bad)
		}
	}
}
