package app

// Being installable and working offline are properties of how the server
// answers, not just of the JavaScript. These pin the parts a change could
// quietly break — after which the app would still look fine online and fail
// the moment someone lost signal.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Every address inside the app must return the same document. If one of them
// starts answering something else, the service worker's cached shell stops
// being a correct answer for it and that page dies offline.
func TestEveryAppAddressReturnsTheSameShell(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	first := bodyOf(t, doGet(t, ts, ck, "/"))
	if !strings.Contains(first, `data-view`) {
		t.Fatal("the shell has nowhere to render into")
	}
	for _, path := range []string{
		"/notes/anything", "/notes/anything/anychapter", "/settings", "/trash", "/types",
		"/notes/a-note-this-server-has-never-heard-of",
	} {
		resp := doGet(t, ts, ck, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s answered %d; it must be the shell", path, resp.StatusCode)
			continue
		}
		if body := bodyOf(t, resp); body != first {
			t.Errorf("%s returned a different document from /", path)
		}
	}
}

// The shell is cached and handed to every page, so anything account-specific
// in it would be shown for the wrong note — and anything readable in it would
// have defeated the encryption.
// The worker caches "/" only when this marker is present; without the guard a
// signed-out landing page gets cached as "the shell" and every navigation
// serves a document the app cannot boot from.
func TestShellIsMarkedAndLandingIsNot(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	if got := doGet(t, ts, ck, "/").Header.Get("X-NG-Shell"); got != "1" {
		t.Errorf("the app shell is not marked: %q", got)
	}
	if got := doGet(t, ts, nil, "/").Header.Get("X-NG-Shell"); got != "" {
		t.Errorf("the signed-out landing page is marked as the shell: %q", got)
	}
	sw := readAsset(t, "static/sw.js")
	if !strings.Contains(sw, `headers.get("X-NG-Shell")`) {
		t.Error("the worker no longer gates the shell cache on the marker")
	}
}

func TestShellCarriesNoContent(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	shell := bodyOf(t, doGet(t, ts, ck, "/"))
	for _, forbidden := range []string{"note-one", "ch-one", "v1."} {
		if strings.Contains(shell, forbidden) {
			t.Errorf("the shell carries %q; it must be identical for every page", forbidden)
		}
	}
}

// A worker may only control pages at or below its own path. Served from
// /static/ it could never take charge of /notes/…, which is every page that
// has to work offline.
func TestServiceWorkerHasRootScope(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	resp := doGet(t, ts, ck, "/sw.js")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/sw.js answered %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Service-Worker-Allowed"); got != "/" {
		t.Errorf("Service-Worker-Allowed = %q, want /", got)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "javascript") {
		t.Errorf("wrong content type: %q", resp.Header.Get("Content-Type"))
	}
	// and it must be reachable before there is a session, or a signed-out
	// visitor could never install the app
	if anon := doGet(t, ts, nil, "/sw.js"); anon.StatusCode != http.StatusOK {
		t.Errorf("signed out, /sw.js answered %d", anon.StatusCode)
	}
}

func TestManifestDescribesAnInstallableApp(t *testing.T) {
	ts, ck, _ := newTestServer(t)
	resp := doGet(t, ts, ck, "/manifest.webmanifest")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("manifest answered %d", resp.StatusCode)
	}
	var manifest struct {
		Name     string `json:"name"`
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Scope    string `json:"scope"`
		Icons    []struct {
			Src   string `json:"src"`
			Sizes string `json:"sizes"`
		} `json:"icons"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, resp)), &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if manifest.Name == "" || manifest.StartURL != "/" || manifest.Scope != "/" {
		t.Fatalf("manifest will not install: %+v", manifest)
	}
	if manifest.Display != "standalone" {
		t.Errorf("display = %q", manifest.Display)
	}
	if len(manifest.Icons) == 0 {
		t.Error("no icons")
	}
}

// The service worker's own rules, checked as text because there is no way to
// run one from here. These are the two that would be catastrophic to get
// wrong: caching the sync conversation, or failing to precache the shell.
// "/" is two documents — the app shell signed in, the landing page signed out
// — and only the server knows which. Answering it from the cache handed a
// signed-out visitor the shell, which found no key and sent them to /login: the
// landing page became unreachable on any device that had once cached the shell,
// with no way out but clearing site data. The network has to decide.
func TestTheShellIsNotServedFromCacheWhileOnline(t *testing.T) {
	sw := readAsset(t, "static/sw.js")

	// Scoped to the shell handler: assets are versioned by the build hash, so
	// cache-first is right for those and wrong only here.
	start := strings.Index(sw, "async function ngShellResponse")
	if start < 0 {
		t.Fatal("ngShellResponse not found")
	}
	end := strings.Index(sw[start:], "\nasync function ")
	if end < 0 {
		t.Fatal("could not find the end of ngShellResponse")
	}
	shell := sw[start : start+end]

	if strings.Contains(shell, "if (cached) {\n\t\tnetwork.catch") {
		t.Error("the worker answers navigations from cache without asking the server; " +
			"a signed-out visitor gets the app shell and is bounced to /login")
	}
	// The cache must remain the answer when the network cannot be reached, or
	// this trades a signed-out bug for a broken offline promise.
	if !strings.Contains(shell, "NG_SHELL_TIMEOUT_MS") {
		t.Error("there is no bound on how long a navigation waits for the server")
	}
	if !strings.Contains(shell, "return cached;") {
		t.Error("the cached shell is no longer the fallback; the app would not open offline")
	}
	// Following the redirect here would render the sign-in page under the
	// address that was asked for, leaving the location bar wrong.
	if !strings.Contains(shell, `redirect: "manual"`) {
		t.Error("a signed-out deep link would render /login under the original address")
	}
}

func TestServiceWorkerRules(t *testing.T) {
	sw := readAsset(t, "static/sw.js")
	if !strings.Contains(sw, `url.pathname === "/sync"`) || !strings.Contains(sw, `url.pathname.startsWith("/sync/")`) {
		t.Error("the worker no longer excludes /sync from caching")
	}
	if !strings.Contains(sw, `"/"`) || !strings.Contains(sw, "NG_PRECACHE") {
		t.Error("the shell is no longer precached")
	}
	// A kept runtime is up to 116 MB. Naming its cache after the build would
	// throw it away on every upgrade.
	if !strings.Contains(sw, `const NG_RUNTIME_CACHE = "ng-runtime";`) {
		t.Error("the runtime cache name is versioned; an upgrade would orphan 149 MB")
	}
	// the runtimes are 149 MB; they must never be swept up by the precache
	if strings.Contains(sw, `"/pyodide/pyodide.asm.wasm",`) &&
		strings.Contains(sw[:strings.Index(sw, "];")], "pyodide.asm.wasm") {
		t.Error("a runtime file crept into the precache list")
	}
	// Assets loaded by other scripts — a module worker, a sandboxed frame —
	// are the ones that get forgotten, because nothing in the shell mentions
	// them and everything works until the network goes away.
	for _, loaded := range []string{
		"/static/typst-worker.js", "/static/runner.html", "/static/runner.js",
	} {
		if !strings.Contains(sw, `"`+loaded+`"`) {
			t.Errorf("%s is loaded at runtime but not precached", loaded)
		}
	}

	// every script the shell loads has to be precached, or the app starts
	// online and breaks offline
	shell := readAsset(t, "templates/app.html")
	for _, line := range strings.Split(shell, "\n") {
		if !strings.Contains(line, `src="/static/`) {
			continue
		}
		start := strings.Index(line, `src="`) + 5
		src := line[start : start+strings.Index(line[start:], `"`)]
		if !strings.Contains(sw, `"`+src+`"`) {
			t.Errorf("%s is loaded by the shell but not precached", src)
		}
	}
}
