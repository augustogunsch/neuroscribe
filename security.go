package main

// Request-level defenses that sit in front of every handler:
//
//   - Host allowlist: blocks DNS rebinding, where a hostile page resolves its
//     own domain to 127.0.0.1 and talks to this server from the victim's
//     browser. Same-origin policy does not help there — the browser believes
//     it is still on the attacker's origin — but the Host header gives it away.
//   - Origin/Referer check plus a double-submit CSRF token on every unsafe
//     method.
//   - A small login throttle (see auth.go for its use).

import (
	"context"
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const csrfCookie = "ng_csrf"

type ctxKey int

const csrfCtxKey ctxKey = iota

// allowedHosts is the set of Host header values this server answers to.
// NEUROSCRIBE_ALLOWED_HOSTS overrides it (comma-separated, no port).
func buildAllowedHosts(addr, override string) map[string]bool {
	hosts := map[string]bool{}
	add := func(h string) {
		h = strings.ToLower(strings.TrimSpace(h))
		if h != "" {
			hosts[h] = true
		}
	}
	if override != "" {
		for _, h := range strings.Split(override, ",") {
			add(h)
		}
		return hosts
	}
	add("localhost")
	add("127.0.0.1")
	add("[::1]")
	add("::1")
	if host, _, err := net.SplitHostPort(addr); err == nil && host != "" && host != "0.0.0.0" && host != "::" {
		add(host)
	}
	return hosts
}

// hostAllowed compares the request Host (minus port) against the allowlist.
func (s *server) hostAllowed(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return s.allowedHosts[strings.ToLower(strings.Trim(host, "[]"))] ||
		s.allowedHosts[strings.ToLower(host)]
}

// originAllowed rejects cross-site unsafe requests. Browsers always send
// Origin on POST; when it is absent (curl, old clients) the CSRF token alone
// carries the request.
func (s *server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return s.allowedHosts[strings.ToLower(strings.Trim(host, "[]"))] ||
		s.allowedHosts[strings.ToLower(host)]
}

// clientIP is for logs only — never for authorization decisions.
func clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// trustProxyHeaders controls whether X-Forwarded-* headers are believed. It
// stays false unless a trusted reverse proxy sits in front and sets them
// (NEUROSCRIBE_TRUST_PROXY): a directly-exposed server must not let any client
// forge its apparent scheme by sending its own X-Forwarded-Proto.
var trustProxyHeaders bool

func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return trustProxyHeaders && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// ensureCSRF returns the request's CSRF token, minting a cookie when missing.
//
// The cookie is deliberately readable by same-origin script. It used to be
// HttpOnly, with the token also printed into every page — which stopped
// working when one cached document started serving every address: a shell
// cached on Monday would carry Monday's token and be refused on Tuesday. A
// double-submit token is not a credential on its own (an attacker on another
// origin can neither read this cookie nor set that header), and it was already
// readable in the page it was printed into.
func (s *server) ensureCSRF(w http.ResponseWriter, r *http.Request) string {
	token := ""
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 64 {
		token = c.Value
	}
	if token == "" {
		token = newSessionToken()
	}
	// Re-sent every time rather than only when missing. A browser that still
	// holds the old HttpOnly cookie would otherwise keep it forever, and the
	// page could never read the token it has to echo back — every sync would
	// fail with 403 and no amount of reloading would fix it.
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   isHTTPS(r),
	})
	return token
}

// checkCSRF validates the submitted token against the cookie (double submit).
func checkCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	sent := r.Header.Get("X-CSRF-Token")
	if sent == "" {
		sent = r.FormValue("csrf_token")
	}
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(sent)) == 1
}

func csrfFromContext(r *http.Request) string {
	v, _ := r.Context().Value(csrfCtxKey).(string)
	return v
}

func withCSRF(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), csrfCtxKey, token))
}

// ---- login throttle ----

const (
	maxLoginFailures = 5
	loginLockout     = 15 * time.Minute
)

type throttle struct {
	mu      sync.Mutex
	entries map[string]*throttleEntry
	max     int
	lockout time.Duration
}

type throttleEntry struct {
	failures int
	until    time.Time
	seen     time.Time
}

func newThrottle(max int, lockout time.Duration) *throttle {
	return &throttle{entries: map[string]*throttleEntry{}, max: max, lockout: lockout}
}

func throttleKey(r *http.Request, username string) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return ip + "|" + strings.ToLower(username)
}

// blocked reports whether this key is locked out right now.
func (t *throttle) blocked(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.entries[key]
	if e == nil || time.Now().After(e.until) {
		return false, 0
	}
	return true, time.Until(e.until)
}

func (t *throttle) fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()
	e := t.entries[key]
	if e == nil {
		e = &throttleEntry{}
		t.entries[key] = e
	}
	e.failures++
	e.seen = time.Now()
	if e.failures >= t.max {
		e.until = time.Now().Add(t.lockout)
	}
}

func (t *throttle) reset(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, key)
}

// sweepLocked drops entries untouched for an hour; called under the lock.
func (t *throttle) sweepLocked() {
	cutoff := time.Now().Add(-time.Hour)
	for k, e := range t.entries {
		if e.seen.Before(cutoff) && time.Now().After(e.until) {
			delete(t.entries, k)
		}
	}
}
