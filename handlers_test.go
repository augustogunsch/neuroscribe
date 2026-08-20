package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

// newTestServer spins up the full HTTP stack on a throwaway database with a
// logged-in user, returning the test server and a session cookie.
func newTestServerFull(t *testing.T) (*httptest.Server, *http.Cookie, *sql.DB, *server) {
	t.Helper()
	db := openDB(filepath.Join(t.TempDir(), "test.db"))
	t.Cleanup(func() { db.Close() })

	keys, _, _ := testAccountKeys(t, "test-password")
	hash, err := bcrypt.GenerateFromPassword([]byte(keys.Get("auth_key")), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, pass_hash, kdf_salt, wrapped_key, email, email_verified)
		VALUES ('tester', ?, ?, ?, 'tester@example.com', 1)`,
		string(hash), keys.Get("kdf_salt"), keys.Get("wrapped_key")); err != nil {
		t.Fatal(err)
	}
	token := newSessionToken()
	if _, err := db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, 1, ?)",
		hashToken(token), time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}

	// no python runtime in tests; a stand-in typesetter directory, so the
	// PDF button and the /typst/ route are exercised the way they ship
	srv := newServer(db, "", fakeTypstDir(t), "127.0.0.1:0")
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, &http.Cookie{Name: sessionCookie, Value: token}, db, srv
}

// newAccount adds a second signed-in account, for the tests that check one
// cannot see the other.
func newAccount(t *testing.T, db *sql.DB, username string) *http.Cookie {
	t.Helper()
	res, err := db.Exec(`INSERT INTO users (username, pass_hash, email_verified)
		VALUES (?, 'x', 1)`, username)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	token := newSessionToken()
	if _, err := db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hashToken(token), uid, time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: token}
}

// newTestServer is the common case: callers that do not need the server value.
func newTestServer(t *testing.T) (*httptest.Server, *http.Cookie, *sql.DB) {
	ts, ck, db, _ := newTestServerFull(t)
	return ts, ck, db
}

// fakeTypstDir is a directory shaped enough for typstDir() to accept it. The
// real one holds ~33 MB fetched by `make typst`.
func fakeTypstDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "typst_ts_web_compiler_bg.wasm"),
		[]byte("\x00asm\x01\x00\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sealedValue is ciphertext-shaped: enough for the server, which only checks
// the envelope because it cannot check the contents.
func sealedValue() string {
	return "v1." + strings.Repeat("A", 16) + "." + strings.Repeat("B", 44)
}

// client that does not follow redirects, so tests can assert on them
func noRedirectClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// csrfFor mints a fresh CSRF cookie/token pair from the server.
func csrfFor(t *testing.T, ts *httptest.Server) *http.Cookie {
	t.Helper()
	resp, err := noRedirectClient().Get(ts.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == csrfCookie {
			return c
		}
	}
	t.Fatal("server did not set a CSRF cookie")
	return nil
}

func doPost(t *testing.T, ts *httptest.Server, ck *http.Cookie, path string, form url.Values) *http.Response {
	t.Helper()
	csrf := csrfFor(t, ts)
	req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(ck)
	req.AddCookie(csrf)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func doGet(t *testing.T, ts *httptest.Server, ck *http.Cookie, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	if ck != nil {
		req.AddCookie(ck)
	}
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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

// signIn mimics the browser: fetch the salt, derive, send only the auth key.
func signIn(t *testing.T, ts *httptest.Server, username, password string) *http.Response {
	t.Helper()
	resp := doGet(t, ts, nil, "/auth/params?username="+username)
	var params struct {
		Salt       string `json:"salt"`
		Iterations int    `json:"iterations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&params); err != nil {
		t.Fatal(err)
	}
	authKey, _ := testDeriveKeys(t, password, params.Salt)
	return doPost(t, ts, &http.Cookie{Name: "x"}, "/login",
		url.Values{"username": {username}, "auth_key": {authKey}})
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

// ---- security regressions ----

func TestHostHeaderRejected(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	req.Host = "evil.example.com" // DNS rebinding: resolves here, wrong name
	req.AddCookie(ck)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host accepted: %d", resp.StatusCode)
	}
}

func TestCSRFRequired(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	// no token at all
	req, _ := http.NewRequest("POST", ts.URL+"/dirs", strings.NewReader("name=X"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(ck)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("request without CSRF token accepted: %d", resp.StatusCode)
	}
	// cookie present but mismatched token
	csrf := csrfFor(t, ts)
	req, _ = http.NewRequest("POST", ts.URL+"/dirs", strings.NewReader("name=X"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", "not-the-right-token")
	req.AddCookie(ck)
	req.AddCookie(csrf)
	resp2, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("mismatched CSRF token accepted: %d", resp2.StatusCode)
	}
}

func TestCrossOriginPostRejected(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	csrf := csrfFor(t, ts)
	req, _ := http.NewRequest("POST", ts.URL+"/dirs", strings.NewReader("name=X"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.Header.Set("Origin", "https://evil.example.com")
	req.AddCookie(ck)
	req.AddCookie(csrf)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin POST accepted: %d", resp.StatusCode)
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

func TestCSPHasNoThirdPartyScripts(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	resp := doGet(t, ts, ck, "/")
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "jsdelivr") {
		t.Fatalf("CSP still allows third-party scripts: %q", csp)
	}
	// the typesetter needs eval to start, so it runs in a worker with its own
	// policy; the page holding the keys must never gain that permission
	if strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("CSP allows eval in the page: %q", csp)
	}
	body := bodyOf(t, resp)
	if strings.Contains(body, "cdn.jsdelivr.net") {
		t.Fatal("page still references the CDN")
	}
	if r := doGet(t, ts, ck, "/static/vendor/katex.min.js"); r.StatusCode != 200 {
		t.Fatalf("vendored katex missing: %d", r.StatusCode)
	}
	if r := doGet(t, ts, ck, "/static/vendor/fonts/KaTeX_Main-Regular.woff2"); r.StatusCode != 200 {
		t.Fatalf("vendored font missing: %d", r.StatusCode)
	}
}

func TestAllowedHosts(t *testing.T) {
	hosts := buildAllowedHosts("127.0.0.1:8484", "")
	for _, h := range []string{"localhost", "127.0.0.1"} {
		if !hosts[h] {
			t.Errorf("default allowlist missing %q", h)
		}
	}
	if hosts["evil.example.com"] {
		t.Error("allowlist too permissive")
	}
	// An override adds the public domain; it must never revoke loopback —
	// the healthcheck, the deploy script and local curl all speak to
	// 127.0.0.1, and rebinding attacks never carry loopback names in Host.
	custom := buildAllowedHosts("0.0.0.0:8484", "notes.example.com, localhost")
	if !custom["notes.example.com"] || !custom["localhost"] || !custom["127.0.0.1"] {
		t.Errorf("override allowlist wrong: %v", custom)
	}
	if custom["evil.example.com"] {
		t.Error("override allowlist too permissive")
	}
}

// ---- public registration ----

// solveAltcha brute-forces a challenge the way the browser widget does and
// returns the base64 payload the form submits.
func solveAltcha(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp, err := noRedirectClient().Get(ts.URL + "/altcha/challenge")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var c altchaChallenge
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	for n := int64(0); n <= int64(c.MaxNumber); n++ {
		if altchaHash(c.Salt, n) == c.Challenge {
			payload, _ := json.Marshal(altchaPayload{
				Algorithm: c.Algorithm, Challenge: c.Challenge,
				Number: n, Salt: c.Salt, Signature: c.Signature,
			})
			return base64.StdEncoding.EncodeToString(payload)
		}
	}
	t.Fatal("challenge unsolvable")
	return ""
}

// withMailCapture switches the server to an in-memory mailer.
func withMailCapture(t *testing.T, ts *httptest.Server, srv *server) *[]string {
	t.Helper()
	var sent []string
	srv.registrationFlag = true
	srv.mail = &mailer{baseURL: ts.URL, from: "test@example.com", sendFunc: func(to, subject, body string) error {
		sent = append(sent, to+"|"+subject+"|"+body)
		return nil
	}}
	return &sent
}

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

// ---- account isolation ----

// addUser creates a second, fully seeded account and returns its session.
func addUser(t *testing.T, db *sql.DB, name string) (*http.Cookie, int64) {
	t.Helper()
	keys, _, _ := testAccountKeys(t, "other-password")
	hash, err := bcrypt.GenerateFromPassword([]byte(keys.Get("auth_key")), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO users (username, pass_hash, kdf_salt, wrapped_key, email_verified)
		VALUES (?, ?, ?, ?, 1)`, name, string(hash), keys.Get("kdf_salt"), keys.Get("wrapped_key"))
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	db.Exec("INSERT INTO note_types (user_id, name_enc, schema_enc) VALUES (?, ?, ?)",
		uid, sealedValue(), sealedValue())
	token := newSessionToken()
	if _, err := db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)",
		hashToken(token), uid, time.Now().UTC().Add(time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookie, Value: token}, uid
}

// ---- end-to-end encryption helpers (mirror of static/crypto.js) ----

// testDeriveKeys reproduces the browser's derivation: PBKDF2 to a master key,
// then two independent HKDF sub-keys — one the server may see, one it may not.
func testDeriveKeys(t *testing.T, password, saltB64 string) (authKey string, encKey []byte) {
	t.Helper()
	salt, err := base64.StdEncoding.DecodeString(saltB64)
	if err != nil {
		t.Fatal(err)
	}
	master := pbkdf2.Key([]byte(password), salt, kdfIterations, 32, sha256.New)
	auth := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte("neuroscribe-auth-v1")), auth); err != nil {
		t.Fatal(err)
	}
	enc := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte("neuroscribe-enc-v1")), enc); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(auth), enc
}

func testSeal(t *testing.T, key []byte, plaintext string) string {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	ct := gcm.Seal(nil, iv, []byte(plaintext), nil)
	return "v1." + base64.StdEncoding.EncodeToString(iv) + "." + base64.StdEncoding.EncodeToString(ct)
}

func testOpen(t *testing.T, key []byte, blob string) string {
	t.Helper()
	parts := strings.Split(blob, ".")
	if len(parts) != 3 || parts[0] != "v1" {
		t.Fatalf("not ciphertext: %q", blob)
	}
	iv, _ := base64.StdEncoding.DecodeString(parts[1])
	ct, _ := base64.StdEncoding.DecodeString(parts[2])
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	return string(plain)
}

// testAccountKeys builds what a browser would send at registration.
func testAccountKeys(t *testing.T, password string) (form url.Values, encKey []byte, dataKey []byte) {
	t.Helper()
	salt := make([]byte, 16)
	rand.Read(salt)
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	authKey, encKey := testDeriveKeys(t, password, saltB64)
	dataKey = make([]byte, 32)
	rand.Read(dataKey)
	wrapped := testSeal(t, encKey, base64.StdEncoding.EncodeToString(dataKey))
	return url.Values{
		"kdf_salt": {saltB64}, "auth_key": {authKey}, "wrapped_key": {wrapped},
	}, encKey, dataKey
}

// ---- the encrypted model ----

// Snippets execute in the browser now. The point of that change is that the
// server has no way to run user code at all, so the route's absence is the
// property worth testing.
func TestServerRunsNoSnippets(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	// 404 or 405 — either way there is no handler behind it. What matters is
	// that nothing accepts code.
	resp := doPost(t, ts, ck, "/run", url.Values{"code": {"print('x')"}})
	if resp.StatusCode < 400 {
		t.Fatalf("POST /run answered %d — the server still executes snippets", resp.StatusCode)
	}
}

// The runner document is the one place eval is allowed. That is only safe
// because it is framed with an opaque origin, so the two must stay together.
func TestRunnerFrameIsIsolated(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	resp := doGet(t, ts, ck, "/static/runner.html")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runner missing: %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "'unsafe-eval'") {
		t.Fatalf("runner cannot host an interpreter without eval: %q", csp)
	}
	// 'self' would resolve to the opaque origin and match nothing; the policy
	// has to name this server instead.
	if strings.Contains(csp, "script-src 'self'") {
		t.Fatalf("runner policy uses 'self' on an opaque origin: %q", csp)
	}
	if !strings.Contains(csp, "frame-ancestors 'self'") {
		t.Fatalf("runner must only be framed by this app: %q", csp)
	}
	if got := resp.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, would block our own frame", got)
	}

	if body := bodyOf(t, resp); !strings.Contains(body, "/static/runner.js") {
		t.Fatal("runner page does not load its script")
	}
	// The page that frames it must sandbox it without allow-same-origin,
	// which is what strips its access to cookies, storage and our DOM.
	run, err := os.ReadFile("static/run.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(run), `setAttribute("sandbox", "allow-scripts")`) {
		t.Fatal("the runner frame is no longer sandboxed to allow-scripts alone")
	}
}

// The typesetter needs eval to start. It gets that on its own response so the
// page never does, which only works while this handler stays in front of the
// generic static one.
func TestTypstWorkerCarriesItsOwnPolicy(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	resp := doGet(t, ts, ck, "/static/typst-worker.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("typst worker missing: %d", resp.StatusCode)
	}
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "'wasm-unsafe-eval'") {
		t.Fatalf("worker cannot start wasm-bindgen without its own grant: %q", csp)
	}
	if !strings.Contains(csp, "default-src 'self'") || strings.Contains(csp, "frame-ancestors") {
		t.Fatalf("worker fell through to the page policy: %q", csp)
	}
	page := doGet(t, ts, ck, "/").Header.Get("Content-Security-Policy")
	if csp == page {
		t.Fatal("worker and page share a policy")
	}
}

// Every runtime file is downloaded at deploy time from a CDN, an npm registry
// and a mutable git branch, and then executed in someone's browser. The pin in
// assets.sha256 is the only thing standing between those two facts, so an
// unpinned download must not be able to creep back into the Makefile.
func TestFetchedAssetsArePinned(t *testing.T) {
	manifest, err := os.ReadFile("assets.sha256")
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]bool{}
	for _, line := range strings.Split(string(manifest), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed manifest line: %q", line)
		}
		if len(fields[0]) != 64 {
			t.Fatalf("not a sha256: %q", fields[0])
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			t.Fatalf("not hex: %q", fields[0])
		}
		pinned[fields[1]] = true
	}

	makefile, err := os.ReadFile("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(makefile), "curl") {
		t.Fatal("the Makefile downloads something directly; route it through scripts/fetch.sh so it is verified")
	}

	// the wheels and core files named in PYODIDE_FILES, and the fonts in
	// TYPST_FONTS, are listed by name rather than fetched from a lock file
	for _, group := range []struct{ variable, dir string }{
		{"PYODIDE_FILES", "pyodide"},
		{"TYPST_FONTS", "typst/fonts"},
	} {
		for _, name := range makeListValue(t, string(makefile), group.variable) {
			if want := group.dir + "/" + name; !pinned[want] {
				t.Errorf("%s is fetched but not pinned in assets.sha256", want)
			}
		}
	}
}

// makeListValue reads a backslash-continued list variable out of the Makefile.
func makeListValue(t *testing.T, makefile, name string) []string {
	t.Helper()
	start := strings.Index(makefile, name+" :=")
	if start < 0 {
		t.Fatalf("%s not found in Makefile", name)
	}
	var body strings.Builder
	for _, line := range strings.Split(makefile[start:], "\n") {
		body.WriteString(strings.TrimSuffix(line, "\\"))
		body.WriteString(" ")
		if !strings.HasSuffix(line, "\\") {
			break
		}
	}
	fields := strings.Fields(body.String())
	return fields[2:] // drop the variable name and ":="
}

// Notes are rendered in the browser now, so DOMPurify is the only thing
// between note content and the page that holds the decryption keys. These
// assert the configuration rather than the library: an allowlist that has been
// loosened back into a denylist is the failure worth catching.
func TestMarkdownSanitizerIsAnAllowlist(t *testing.T) {
	src, err := os.ReadFile("static/render.js")
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)

	for _, required := range []string{
		"ALLOWED_TAGS:", "ALLOWED_ATTR:", "ALLOWED_URI_REGEXP:",
		// app.js dispatches clicks on [data-action]; content carrying one
		// would be driving the app
		"ALLOW_DATA_ATTR: false",
		// re-serializing sanitized output is what mutation-XSS needs
		"RETURN_DOM_FRAGMENT: true",
	} {
		if !strings.Contains(js, required) {
			t.Errorf("the markdown sanitizer no longer sets %s", required)
		}
	}
	// A denylist cannot bound what future browsers add.
	if strings.Contains(js, "FORBID_TAGS") || strings.Contains(js, "USE_PROFILES") {
		t.Error("the sanitizer is back to forbidding named tags instead of allowing named ones")
	}
	// The allowlist must not readmit the tags this app has no use for.
	for _, tag := range []string{`"svg"`, `"math"`, `"iframe"`, `"style"`, `"form"`, `"script"`} {
		if strings.Contains(allowedTagsBlock(t, js), tag) {
			t.Errorf("%s is allowed in rendered notes", tag)
		}
	}
	if strings.Contains(js, "target.innerHTML = ") {
		t.Error("sanitized markdown is assigned as a string again")
	}
}

// allowedTagsBlock returns the NG_ALLOWED_TAGS literal.
func allowedTagsBlock(t *testing.T, js string) string {
	t.Helper()
	start := strings.Index(js, "NG_ALLOWED_TAGS = [")
	if start < 0 {
		t.Fatal("NG_ALLOWED_TAGS not found")
	}
	end := strings.Index(js[start:], "]")
	if end < 0 {
		t.Fatal("NG_ALLOWED_TAGS is not terminated")
	}
	return js[start : start+end]
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
