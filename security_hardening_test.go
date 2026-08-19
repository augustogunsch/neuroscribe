package main

// Regression tests for the 2026 security hardening pass: per-account isolation
// on blobs and the trash sweep, session expiry, the client-timestamp clamp, and
// a byte-for-byte tamper check on the vendored browser libraries.

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

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
