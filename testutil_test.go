package main

// The test harness: a real server on a real socket, a real database, and the
// handful of helpers every test here leans on.
//
// Nothing is mocked. What these tests are for is what an actual client gets
// back over an actual connection — headers, cookies, status codes — and a mock
// of this server would only ever agree with whatever the tests assumed.
//
// testDeriveKeys and testSeal mirror static/crypto.js on purpose. Reusing the
// server's idea of the client's cryptography would prove the two agree with
// themselves rather than with each other.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// blobPut uploads bytes for a ref as a given account, returning the status.
func blobPut(t *testing.T, ts *httptest.Server, ck *http.Cookie, ref, body string) int {
	t.Helper()
	csrf := csrfFor(t, ts)
	req, _ := http.NewRequest("PUT", ts.URL+"/sync/blob/"+ref, strings.NewReader(body))
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(ck)
	req.AddCookie(csrf)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// blobGet fetches a ref as a given account, returning status and body bytes.
func blobGet(t *testing.T, ts *httptest.Server, ck *http.Cookie, ref string) (int, string) {
	t.Helper()
	resp := doGet(t, ts, ck, "/sync/blob/"+ref)
	return resp.StatusCode, bodyOf(t, resp)
}
