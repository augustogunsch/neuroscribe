package app

// Serving the browser's code runtime.
//
// Snippets never run on the server. Python is CPython compiled to WebAssembly
// and JavaScript is the browser's own engine, both driven from a sandboxed
// frame (static/runner.js); this file only hands out the runtime files.
//
// That trade is worth stating plainly. The server has no way to execute
// anything a user writes — no container runtime, no daemon socket, no process
// spawned from note content — and in exchange it serves a few hundred megabytes
// of static assets. The snippet, its output and any data it touches stay in the
// browser, which is the same place the decryption keys live.

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// pyodideDir returns the directory holding the runtime, or "" when it has not
// been fetched (see `make pyodide`). Missing files are not fatal: everything
// except running Python keeps working.
func pyodideDir() string { return pyodideDirIn(envOr("PYODIDE_DIR", "pyodide")) }

// typstDir returns the directory holding the typesetter, or "" when it has not
// been fetched (see `make typst`). As with Python, its absence costs one
// feature — PDF export — and nothing else.
func typstDir() string { return typstDirIn(envOr("TYPST_DIR", "typst")) }

func pyodideDirIn(dir string) string { return runtimeDirIn(dir, "pyodide.js") }

func typstDirIn(dir string) string { return runtimeDirIn(dir, "typst_ts_web_compiler_bg.wasm") }

// runtimeDirIn reports an absolute path to dir when the file that proves the
// runtime was actually fetched is present, and "" otherwise.
func runtimeDirIn(dir, sentinel string) string {
	if _, err := os.Stat(filepath.Join(dir, sentinel)); err != nil {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// pyodideAssets serves the runtime to the sandboxed frame. That frame has an
// opaque origin, so every fetch it makes here is cross-origin and needs CORS
// to be allowed at all — safe to grant, since these are public files that
// carry no user data and are read with no credentials.
func pyodideAssets(dir string) http.Handler {
	files := runtimeAssets(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		files.ServeHTTP(w, r)
	})
}

// runtimeAssets serves a fetched runtime directory. Unlike the Pyodide frame,
// the typesetter is loaded by the page itself, so it needs no CORS grant.
func runtimeAssets(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// The one file that is never cached, because it is what says whether
		// everything else still may be. A runtime is fetched with its version
		// on the end of the address, so changing any byte changes every
		// address — but only if this answer is fresh. Cache it, and a browser
		// keeps last month's runtime for a year; nothing about that failure
		// looks like a caching problem from the outside, which is exactly what
		// makes it expensive.
		// the route strips the /typst/ prefix, so this arrives as a bare
		// name rather than a path
		if path.Base(r.URL.Path) == "manifest.json" {
			h.Set("Cache-Control", "no-cache")
		} else {
			h.Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, ".wasm"):
			h.Set("Content-Type", "application/wasm")
		case strings.HasSuffix(r.URL.Path, ".mjs"):
			h.Set("Content-Type", "text/javascript; charset=utf-8")
		}
		files.ServeHTTP(w, r)
	})
}

// runnerCSP is the policy for static/runner.html alone.
//
// It has to be looser than the rest of the site — a WebAssembly interpreter
// needs wasm compilation, and running a JavaScript snippet is eval by
// definition — which is exactly why that document is framed with an opaque
// origin and holds no session, no keys and no DOM of ours.
//
// 'self' cannot be used here: an opaque origin matches nothing, so the policy
// names this server explicitly instead.
func runnerCSP(r *http.Request) string {
	origin := requestOrigin(r)
	return "default-src 'none'; " +
		"script-src " + origin + " 'unsafe-eval' 'wasm-unsafe-eval'; " +
		"connect-src " + origin + "; " +
		"worker-src blob:; " +
		"child-src blob:; " +
		"form-action 'none'; base-uri 'none'; " +
		"frame-ancestors 'self'"
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
