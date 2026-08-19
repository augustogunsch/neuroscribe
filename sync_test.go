package main

// The sync API is now the whole of what the server does with a person's
// writing, so these are the tests that matter most: that it keeps accounts
// apart, that it refuses anything readable, and that two devices editing the
// same thing cannot silently lose one of the edits.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func sealed(text string) string {
	// the shape the browser produces; the bytes are not real ciphertext, and
	// the server is not able to tell the difference — which is the point
	return fmt.Sprintf(`{"h":"v1.%s.%s"}`, "AAAAAAAAAAAAAAAA", text)
}

func push(t *testing.T, ts *httptest.Server, ck *http.Cookie, records []syncRecord) pushResponse {
	t.Helper()
	body, _ := json.Marshal(pushRequest{Records: records})
	csrf := csrfFor(t, ts)
	req, _ := http.NewRequest("POST", ts.URL+"/sync", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", csrf.Value)
	req.AddCookie(ck)
	req.AddCookie(csrf)
	resp, err := noRedirectClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out pushResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("push response: %v", err)
	}
	resp.Body.Close()
	return out
}

func pull(t *testing.T, ts *httptest.Server, ck *http.Cookie, since int64) pullResponse {
	t.Helper()
	resp := doGet(t, ts, ck, fmt.Sprintf("/sync?since=%d", since))
	var out pullResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("pull response: %v", err)
	}
	resp.Body.Close()
	return out
}

// A record that arrives readable is a client bug, and storing it would make
// that bug permanent and invisible.
func TestSyncRefusesPlaintext(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	out := push(t, ts, ck, []syncRecord{
		{Ref: "plain1", Kind: "note", Payload: `{"h":"Dear diary"}`},
		{Ref: "plain2", Kind: "note", Payload: "not even json"},
		{Ref: "ok1", Kind: "note", Payload: sealed("Zm9v")},
	})
	if len(out.Rejected) != 2 {
		t.Fatalf("expected both plaintext records refused, got %+v", out.Rejected)
	}
	if len(out.Applied) != 1 || out.Applied[0].Ref != "ok1" {
		t.Fatalf("the sealed record should have been stored: %+v", out.Applied)
	}
}

// The pull cursor is the only thing standing between two accounts.
func TestSyncKeepsAccountsApart(t *testing.T) {
	ts, ck, db := newTestServer(t)
	push(t, ts, ck, []syncRecord{{Ref: "mine", Kind: "note", Payload: sealed("bWluZQ")}})

	other := newAccount(t, db, "intruder")
	page := pull(t, ts, other, 0)
	if len(page.Records) != 0 {
		t.Fatalf("another account can read these records: %+v", page.Records)
	}
	// and cannot overwrite one either: refs are per account, so this makes a
	// record of their own rather than touching ours
	push(t, ts, other, []syncRecord{{Ref: "mine", Kind: "note", Payload: sealed("dGhlaXJz")}})
	mine := pull(t, ts, ck, 0)
	if len(mine.Records) != 1 || !strings.Contains(mine.Records[0].Payload, "bWluZQ") {
		t.Fatalf("our record was disturbed: %+v", mine.Records)
	}
}

// Pulling with a cursor must return every change after it, exactly once, in
// order — a device that misses one has silently lost a note.
func TestSyncCursorIsComplete(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	for i := 0; i < 5; i++ {
		push(t, ts, ck, []syncRecord{{Ref: fmt.Sprintf("r%d", i), Kind: "note", Payload: sealed("eA")}})
	}
	page := pull(t, ts, ck, 0)
	if len(page.Records) != 5 {
		t.Fatalf("expected 5 records, got %d", len(page.Records))
	}
	var last int64
	for _, rec := range page.Records {
		if rec.Seq <= last {
			t.Fatalf("records out of order: %d after %d", rec.Seq, last)
		}
		last = rec.Seq
	}
	// a device already up to date asks again and is told nothing
	if again := pull(t, ts, ck, page.Cursor); len(again.Records) != 0 {
		t.Fatalf("a caught-up device was sent %d records again", len(again.Records))
	}
	// one more change, and only that one comes back
	push(t, ts, ck, []syncRecord{{Ref: "r0", Kind: "note", BaseRev: 1, Payload: sealed("eQ")}})
	tail := pull(t, ts, ck, page.Cursor)
	if len(tail.Records) != 1 || tail.Records[0].Ref != "r0" {
		t.Fatalf("expected just the edited record, got %+v", tail.Records)
	}
}

// Two devices editing the same chapter is the case where data gets lost
// quietly. The second write must be refused, not applied.
func TestSyncRefusesAStaleWrite(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	push(t, ts, ck, []syncRecord{{Ref: "shared", Kind: "chapter", Payload: sealed("Zmlyc3Q")}})

	// device A edits, based on rev 1
	first := push(t, ts, ck, []syncRecord{
		{Ref: "shared", Kind: "chapter", BaseRev: 1, Payload: sealed("QQ")}})
	if len(first.Applied) != 1 {
		t.Fatalf("the first edit should have been accepted: %+v", first)
	}
	// device B was offline and still thinks it is rev 1
	second := push(t, ts, ck, []syncRecord{
		{Ref: "shared", Kind: "chapter", BaseRev: 1, Payload: sealed("Qg")}})
	if len(second.Applied) != 0 {
		t.Fatal("a stale write was applied over a newer one")
	}
	if len(second.Conflicts) != 1 {
		t.Fatalf("the stale write should come back as a conflict: %+v", second)
	}
	if !strings.Contains(second.Conflicts[0].Payload, "QQ") {
		t.Fatal("the conflict did not carry the version actually stored")
	}
}

// Deleting has to travel, or a device that was offline resurrects what another
// device removed.
func TestSyncCarriesDeletions(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	push(t, ts, ck, []syncRecord{{Ref: "doomed", Kind: "note", Payload: sealed("eA")}})
	push(t, ts, ck, []syncRecord{{Ref: "doomed", Kind: "note", BaseRev: 1, Deleted: true}})

	page := pull(t, ts, ck, 0)
	if len(page.Records) != 1 || !page.Records[0].Deleted {
		t.Fatalf("the tombstone did not come back: %+v", page.Records)
	}
}

// Plans are the one judgement the server still makes about content it cannot
// read, and it has to make it before the record exists.
func TestSyncEnforcesTheNoteQuota(t *testing.T) {
	ts, ck, db := newTestServer(t)
	db.Exec("UPDATE users SET plan = 'free'")
	limit := plans["free"].MaxNotes

	records := make([]syncRecord, limit+5)
	for i := range records {
		records[i] = syncRecord{Ref: fmt.Sprintf("n%d", i), Kind: "note", Payload: sealed("eA")}
	}
	out := push(t, ts, ck, records)
	if len(out.Applied) != limit {
		t.Fatalf("stored %d notes against a limit of %d", len(out.Applied), limit)
	}
	if len(out.Rejected) != 5 {
		t.Fatalf("expected 5 refusals, got %d", len(out.Rejected))
	}
}

// An unknown kind is a bug on some device; storing it would spread the bug to
// every other one.
func TestSyncRefusesUnknownKinds(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	out := push(t, ts, ck, []syncRecord{{Ref: "weird", Kind: "spreadsheet", Payload: sealed("eA")}})
	if len(out.Rejected) != 1 || len(out.Applied) != 0 {
		t.Fatalf("an unknown kind was accepted: %+v", out)
	}
}

// Image bytes are only accepted for an image record the account has already
// synced. Without that, the blob store is an unmetered disk.
func TestBlobRequiresAnImageRecord(t *testing.T) {
	ts, ck, _ := newTestServer(t)

	put := func(ref string) int {
		csrf := csrfFor(t, ts)
		req, _ := http.NewRequest("PUT", ts.URL+"/sync/blob/"+ref, strings.NewReader("sealed-bytes"))
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

	if code := put("madeup01"); code != http.StatusNotFound {
		t.Fatalf("a blob with no record was accepted: %d", code)
	}
	push(t, ts, ck, []syncRecord{{Ref: "img00001", Kind: "image", Payload: sealed("bWV0YQ")}})
	if code := put("img00001"); code != http.StatusNoContent {
		t.Fatalf("a blob for a real image record was refused: %d", code)
	}
	// deleting the record closes the door again
	push(t, ts, ck, []syncRecord{{Ref: "img00001", Kind: "image", BaseRev: 1, Deleted: true}})
	if code := put("img00001"); code != http.StatusNotFound {
		t.Fatalf("a blob for a deleted image was accepted: %d", code)
	}
}

// Per-kind quotas alone would leave chapters, folders and types unbounded.
func TestSyncEnforcesTheTotalRecordCap(t *testing.T) {
	ts, ck, db := newTestServer(t)
	// shrink the ceiling rather than pushing 25k records through a test
	db.Exec("UPDATE users SET plan = 'free'")
	old := plans["free"]
	small := old
	small.MaxRecords = 10
	plans["free"] = small
	t.Cleanup(func() { plans["free"] = old })

	records := make([]syncRecord, 15)
	for i := range records {
		records[i] = syncRecord{Ref: fmt.Sprintf("ch%06d", i), Kind: "chapter", Payload: sealed("eA")}
	}
	out := push(t, ts, ck, records)
	if len(out.Applied) != 10 {
		t.Fatalf("stored %d records against a cap of 10", len(out.Applied))
	}
	if len(out.Rejected) != 5 {
		t.Fatalf("expected 5 refusals, got %d", len(out.Rejected))
	}
}

// Addresses and timestamps come from clients, so their shape is the server's
// problem: a ref outside base64url or a garbage timestamp must not be stored.
func TestSyncValidatesClientShapes(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	out := push(t, ts, ck, []syncRecord{
		{Ref: "../../../etc", Kind: "note", Payload: sealed("eA")},
		{Ref: "has spaces", Kind: "note", Payload: sealed("eA")},
		{Ref: "goodref1", Kind: "note", Payload: sealed("eA"), UpdatedAt: "DROP TABLE haha ha"},
	})
	if len(out.Rejected) != 2 {
		t.Fatalf("malformed refs were accepted: %+v", out)
	}
	if len(out.Applied) != 1 {
		t.Fatalf("the valid record should have been stored: %+v", out)
	}
	page := pull(t, ts, ck, 0)
	if got := page.Records[0].UpdatedAt; !timeShape.MatchString(got) {
		t.Fatalf("a garbage timestamp was stored verbatim: %q", got)
	}
}

// The sync responses carry ciphertext and account facts; nothing between the
// server and the browser may keep a copy.
func TestSyncResponsesAreNoStore(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	for _, path := range []string{"/sync?since=0", "/account"} {
		resp := doGet(t, ts, ck, path)
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s served with Cache-Control %q, want no-store", path, got)
		}
	}
}
