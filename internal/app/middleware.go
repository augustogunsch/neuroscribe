package app

// What every request passes through before it reaches a route.
//
// The order here is the security argument, and it is deliberate: the Host is
// checked before anything is served, because the runner document's policy is
// built from it; the body is capped before it is read; the CSRF token is
// minted before the check that needs it; and the session is looked up last,
// so an unauthenticated request has already been through everything cheap.

import (
	"net/http"
	"strings"
)

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	// this app has no use for any of these, so no embedded thing gets them
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	// a window this app opens can never script back into it, and vice versa
	h.Set("Cross-Origin-Opener-Policy", "same-origin")

	// DNS rebinding: a hostile page pointing its own name at this address still
	// sends that name in Host. Checked before anything is served — including the
	// runner document below, whose CSP is built from the request Host.
	if !s.hostAllowed(r) {
		http.Error(w, "unrecognized Host header", http.StatusMisdirectedRequest)
		return
	}

	// The snippet runner is the one document meant to be framed, and the one
	// that needs eval — on an origin of its own, with no session behind it.
	if r.URL.Path == "/static/runner.html" {
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Content-Security-Policy", runnerCSP(r))
		s.mux.ServeHTTP(w, r)
		return
	}
	// everything is served from this origin (every library is vendored), so
	// no third-party host may execute script here. 'unsafe-inline' remains
	// for style only: KaTeX sets inline styles on the spans it renders.
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			// blob: because decrypted images exist only as object URLs this
			// page mints itself — nothing remote gets in through it
			"font-src 'self'; img-src 'self' data: blob:; connect-src 'self'; "+
			// the Altcha widget solves its proof of work in blob workers
			"worker-src 'self' blob:; "+
			// the snippet runner, framed from this origin and sandboxed
			"frame-src 'self'; "+
			// the installed app's own manifest
			"manifest-src 'self'; "+
			"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")

	// Sync is the only thing that carries bulk now: a batch of sealed records,
	// or one image. Everything else is a form.
	limit := int64(1 << 20)
	switch {
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
	case strings.HasPrefix(r.URL.Path, "/sync/blob/"):
		limit = maxBlobBody
	case r.URL.Path == "/sync":
		limit = maxSyncBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)

	token := s.ensureCSRF(w, r)
	r = withCSRF(r, token)
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if !s.originAllowed(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if !checkCSRF(r) {
			http.Error(w, "invalid or missing CSRF token, reload the page and try again",
				http.StatusForbidden)
			return
		}
	}
	r, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	s.mux.ServeHTTP(w, r)
}
