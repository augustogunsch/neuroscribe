package main

// The assertions that exist because of a specific way this could go wrong.
//
// Cross-account isolation, the response headers, the sandbox around snippets,
// the allowlist the sanitizer is built on, and the pins on every vendored
// file. Each one is cheap to keep and would have been expensive to be missing.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

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

// One account's tombstone must never delete another account's live blob. Refs
// are per-account and client-chosen, so a shared ref is legitimate; the trash
// sweep has to scope its delete by (user_id, ref), not by ref alone.
func TestPurgeTrashKeepsAccountsApart(t *testing.T) {
	ts, ck, db := newTestServer(t)
	intruder := newAccount(t, db, "intruder")

	const ref = "shareref"
	// the victim owns a real image and its bytes
	push(t, ts, ck, []syncRecord{{Ref: ref, Kind: "image", Payload: sealed("bWV0YQ")}})
	if code := blobPut(t, ts, ck, ref, "victim-bytes"); code != http.StatusNoContent {
		t.Fatalf("victim could not store its own blob: %d", code)
	}
	// the attacker pushes a long-dead tombstone under the very same ref
	push(t, ts, intruder, []syncRecord{
		{Ref: ref, Kind: "image", Deleted: true, UpdatedAt: "2000-01-01 00:00:00"}})

	purgeTrash(db)

	if code, body := blobGet(t, ts, ck, ref); code != http.StatusOK || body != "victim-bytes" {
		t.Fatalf("the victim's blob was destroyed by another account's tombstone: %d %q", code, body)
	}
}

// A blob is readable and writable only by its owner, even when two accounts
// pick the same ref.
func TestBlobKeepsAccountsApart(t *testing.T) {
	ts, ck, db := newTestServer(t)
	intruder := newAccount(t, db, "intruder")

	const ref = "imgshare"
	push(t, ts, ck, []syncRecord{{Ref: ref, Kind: "image", Payload: sealed("bWV0YQ")}})
	if code := blobPut(t, ts, ck, ref, "victim-secret"); code != http.StatusNoContent {
		t.Fatalf("victim PUT failed: %d", code)
	}

	// the intruder cannot read the victim's bytes...
	if code, body := blobGet(t, ts, intruder, ref); code == http.StatusOK {
		t.Fatalf("another account read the victim's blob: %d %q", code, body)
	}
	// ...nor overwrite them without an image record of their own...
	if code := blobPut(t, ts, intruder, ref, "evil"); code != http.StatusNotFound {
		t.Fatalf("intruder PUT under a ref they do not own was accepted: %d", code)
	}
	// ...and when they do make their own record at the same ref, it is a
	// separate row that leaves the victim's bytes untouched.
	push(t, ts, intruder, []syncRecord{{Ref: ref, Kind: "image", Payload: sealed("b3RoZXI")}})
	if code := blobPut(t, ts, intruder, ref, "evil"); code != http.StatusNoContent {
		t.Fatalf("intruder could not store their own blob: %d", code)
	}
	if code, body := blobGet(t, ts, ck, ref); code != http.StatusOK || body != "victim-secret" {
		t.Fatalf("the victim's blob was overwritten across accounts: %d %q", code, body)
	}
}

// An expired session is no session: a request carrying one must be bounced to
// sign-in, not served.
func TestExpiredSessionRejected(t *testing.T) {
	ts, _, db := newTestServer(t)
	token := newSessionToken()
	if _, err := db.Exec("INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, 1, ?)",
		hashToken(token), time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
	expired := &http.Cookie{Name: sessionCookie, Value: token}
	// /account is auth-gated; an expired cookie must not reach it
	resp := doGet(t, ts, expired, "/account")
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an expired session was accepted on /account (status %d)", resp.StatusCode)
	}
}

// A client may date a record in the past (it was written offline), but never in
// the future: a forward-dated record would sort ahead of everything forever.
func TestSyncClampsFutureTimestamp(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	future := time.Now().UTC().Add(72 * time.Hour).Format("2006-01-02 15:04:05")
	push(t, ts, ck, []syncRecord{{Ref: "future01", Kind: "note", Payload: sealed("eA"), UpdatedAt: future}})
	page := pull(t, ts, ck, 0)
	if len(page.Records) != 1 {
		t.Fatalf("expected the record back, got %+v", page.Records)
	}
	if got := page.Records[0].UpdatedAt; got >= future {
		t.Fatalf("a future timestamp was stored unclamped: %q (>= %q)", got, future)
	}
}

// The vendored browser libraries are embedded in the binary and are the last
// line before a decrypted note reaches the page (DOMPurify especially). This
// asserts the shipped bytes still match assets.sha256 — a swapped purify.min.js
// must fail the build, not sail through gofmt/vet/test.
func TestVendoredAssetsMatchPins(t *testing.T) {
	pins, err := os.Open("assets.sha256")
	if err != nil {
		t.Fatalf("cannot read assets.sha256: %v", err)
	}
	defer pins.Close()

	checked := 0
	scanner := bufio.NewScanner(pins)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "static/vendor/") {
			continue
		}
		wantHash, path := fields[0], fields[1]
		body, err := assets.ReadFile(path)
		if err != nil {
			t.Errorf("pinned vendor file is not embedded: %s (%v)", path, err)
			continue
		}
		sum := sha256.Sum256(body)
		if got := hex.EncodeToString(sum[:]); got != wantHash {
			t.Errorf("vendored %s does not match its pin:\n  embedded %s\n  pinned   %s",
				path, got, wantHash)
		}
		checked++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading assets.sha256: %v", err)
	}
	if checked == 0 {
		t.Fatal("no static/vendor pins found in assets.sha256 — the tamper check verified nothing")
	}
	t.Logf("verified %d vendored assets against their pins", checked)
}
