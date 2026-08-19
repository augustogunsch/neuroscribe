package main

// Typesetting happens in the browser. These tests pin down what that buys:
// the server must have no way to be handed a note, and the permission the
// compiler needs must not reach the page that holds the keys.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestServerWillNotTypesetANote(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	// no endpoint takes a note in the clear, whoever asks and however
	// plausible the route looks
	resp := doPost(t, ts, ck, "/notes/1/pdf", url.Values{})
	if resp.StatusCode < 400 {
		t.Fatalf("the server still accepts a note to typeset: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.Contains(ct, "pdf") {
		t.Fatalf("the server returned a PDF: %q", ct)
	}
}

// The worker's own policy is checked in TestTypstWorkerCarriesItsOwnPolicy.
// What matters here is the other half of that bargain: the permission the
// compiler needs must never appear on the page holding the decryption keys.
func TestEvalNeverReachesThePage(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	for _, path := range []string{"/", "/notes/note-one", "/settings"} {
		csp := doGet(t, ts, ck, path).Header.Get("Content-Security-Policy")
		if strings.Contains(strings.ReplaceAll(csp, "'wasm-unsafe-eval'", ""), "unsafe-eval") {
			t.Fatalf("%s allows eval: %q", path, csp)
		}
	}
}

func TestTypstRuntimeIsServedToMembersOnly(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	const path = "/typst/typst_ts_web_compiler_bg.wasm"

	resp := doGet(t, ts, ck, path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("runtime not served to a member: %d", resp.StatusCode)
	}
	// browsers refuse to stream-compile anything not typed as wasm
	if ct := resp.Header.Get("Content-Type"); ct != "application/wasm" {
		t.Fatalf("wasm served as %q", ct)
	}

	// 33 MB is not something to hand out to visitors
	if anon := doGet(t, ts, nil, path); anon.StatusCode == http.StatusOK {
		t.Fatal("the runtime is served without a session")
	}
}

func TestPDFExportOfferedWhenTypesetterPresent(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	// Pages are built in the browser now, so the shell does not contain a PDF
	// button — it carries the fact the button is drawn from.
	shell := bodyOf(t, doGet(t, ts, ck, "/"))
	if !strings.Contains(shell, "data-typst") {
		t.Fatal("the shell does not tell the browser a typesetter is available")
	}
	if !strings.Contains(readAsset(t, "static/views.js"), "ngExportNotePDF") {
		t.Fatal("the note view no longer offers PDF export")
	}
}
