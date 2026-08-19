package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
)

const (
	maxTitleLen = 200
)

type server struct {
	db            *sql.DB
	pyodideDir    string
	assetVersion  string
	typstDir      string
	mux           *http.ServeMux
	pages         map[string]*template.Template
	partial       *template.Template
	chroma        []byte
	allowedHosts  map[string]bool
	loginTries    *throttle
	registerTries *throttle
	mail          *mailer
	altcha        *altchaVerifier
	// public sign-ups need an explicit opt-in, not just a working mailer
	registrationFlag bool
}

func newServer(db *sql.DB, pyodideDir, typstDir string, addr string) *server {
	// Believe X-Forwarded-* only when a trusted proxy is declared in front.
	trustProxyHeaders = envOr("TRUST_PROXY", "") != ""
	s := &server{
		db:               db,
		pyodideDir:       pyodideDir,
		typstDir:         typstDir,
		mux:              http.NewServeMux(),
		pages:            map[string]*template.Template{},
		chroma:           chromaCSS(),
		assetVersion:     buildVersion(),
		allowedHosts:     buildAllowedHosts(addr, envOr("ALLOWED_HOSTS", "")),
		loginTries:       newThrottle(maxLoginFailures, loginLockout),
		registerTries:    newThrottle(registerPerWindow, registerWindow),
		mail:             newMailer(envOr("BASE_URL", "http://"+addr)),
		altcha:           newAltchaVerifier(altchaKey(db)),
		registrationFlag: envOr("REGISTRATION", "") == "open",
	}
	// parsed with placeholders; each render rebinds them to the viewer's
	// own language and theme (see funcsFor)
	fm := baseFuncs(defaultLang, defaultTheme)
	// Three server-rendered pages remain: the shell the app lives in, and the
	// two that exist before there is an account to render anything for.
	for _, page := range []string{"app.html", "login.html", "register.html", "landing.html"} {
		s.pages[page] = template.Must(template.New("").Funcs(fm).ParseFS(assets, "templates/"+page))
	}

	m := s.mux
	m.HandleFunc("GET /login", s.showLogin)
	m.HandleFunc("POST /login", s.doLogin)
	m.HandleFunc("GET /auth/params", s.authParams)
	m.HandleFunc("POST /logout", s.doLogout)
	m.HandleFunc("GET /register", s.showRegister)
	m.HandleFunc("POST /register", s.doRegister)
	m.HandleFunc("POST /register/resend", s.resendVerification)
	m.HandleFunc("GET /verify", s.verifyEmail)
	m.HandleFunc("GET /altcha/challenge", s.altchaChallengeHandler)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.db.PingContext(r.Context()); err != nil {
			httpError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})
	// confirms the password before the browser changes its local PIN lock
	m.HandleFunc("POST /auth/verify", s.authVerify)
	// rotates the password: old auth key proven, new salt + auth key +
	// re-wrapped data key installed, other sessions revoked
	m.HandleFunc("POST /auth/password", s.changePassword)

	// ---- the sync API: the whole of what this server does with user data ----
	m.HandleFunc("GET /sync", s.syncPull)
	m.HandleFunc("POST /sync", s.syncPush)
	m.HandleFunc("GET /sync/blob/{ref}", s.syncBlob)
	m.HandleFunc("PUT /sync/blob/{ref}", s.syncBlob)
	// what the browser cannot work out for itself: the plan it is held to and
	// which optional runtimes this server has
	m.HandleFunc("GET /account", s.accountInfo)
	m.HandleFunc("POST /account/prefs", s.savePrefs)
	// every translated string, so the interface has a language offline too
	m.HandleFunc("GET /strings/{lang}", s.serveStrings)

	// ---- being an installable app ----
	m.HandleFunc("GET /manifest.webmanifest", s.serveManifest)
	m.HandleFunc("GET /sw.js", s.serveServiceWorker)

	// The typesetter is the one script here allowed to build code from a
	// string: wasm-bindgen's start-up needs it. It gets that permission on its
	// own response, so it stays out of the page's policy — the worker has no
	// DOM, no cookie and no keys, and still cannot reach another origin.
	m.HandleFunc("GET /static/typst-worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-eval' 'wasm-unsafe-eval'; "+
				"connect-src 'self'; worker-src 'self'; base-uri 'none'")
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		noCacheStatic(http.FileServerFS(assets)).ServeHTTP(w, r)
	})
	m.HandleFunc("GET /static/chroma.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(s.chroma)
	})
	// Assets are embedded, so they change only when the binary does — but a
	// cached crypto.js after an upgrade is a genuinely bad failure, so ask
	// browsers to revalidate rather than guess.
	m.Handle("GET /static/", noCacheStatic(http.FileServerFS(assets)))
	// Every remaining address is a page of the app, and every page of the app
	// is the same document: the browser reads its own store and renders from
	// there. A note's address is meaningful to the client and opaque here.
	m.HandleFunc("GET /", s.appShell)
	// The Python runtime is ~116 MB of WebAssembly and wheels, so it is served
	// from disk rather than embedded in the binary, and it is the one thing
	// here worth caching hard: the files are versioned and never change.
	if s.pyodideDir != "" {
		m.Handle("GET /pyodide/", http.StripPrefix("/pyodide/", pyodideAssets(s.pyodideDir)))
	}
	// Same bargain for the typesetter: ~33 MB of WebAssembly and fonts, served
	// from disk so that turning a note into a PDF never needs the note.
	if s.typstDir != "" {
		m.Handle("GET /typst/", http.StripPrefix("/typst/", runtimeAssets(s.typstDir)))
	}
	return s
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	// this app has no use for any of these, so no embedded thing gets them
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	// a window this app opens can never script back into it, and vice versa
	h.Set("Cross-Origin-Opener-Policy", "same-origin")

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
			"font-src 'self'; img-src 'self' data:; connect-src 'self'; "+
			// the Altcha widget solves its proof of work in blob workers
			"worker-src 'self' blob:; "+
			// the snippet runner, framed from this origin and sandboxed
			"frame-src 'self'; "+
			// the installed app's own manifest
			"manifest-src 'self'; "+
			"form-action 'self'; base-uri 'none'; frame-ancestors 'none'")

	// DNS rebinding: a hostile page pointing its own name at this address
	// still sends that name in Host.
	if !s.hostAllowed(r) {
		http.Error(w, "unrecognized Host header", http.StatusMisdirectedRequest)
		return
	}

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

// ---- view data ----
//
// One page is served for every address the app has. It carries no content:
// the browser reads notes from its own store and builds the page, which is
// what lets it do so with the network off. What the shell does carry is the
// handful of things that must be true before any JavaScript runs — who is
// signed in, the CSRF token, which optional runtimes this server has.

type appShell struct {
	Username string
	CSRF     string
	RunnerOK bool
	PDFOK    bool
}

func (s *server) shell(r *http.Request) appShell {
	return appShell{
		Username: s.currentUser(r),
		CSRF:     csrfFromContext(r),
		RunnerOK: s.pyodideDir != "",
		PDFOK:    s.typstDir != "",
	}
}

func (s *server) render(w http.ResponseWriter, r *http.Request, tmpl *template.Template, name string, data any) {
	lang, theme := s.prefs(r)
	clone, err := tmpl.Clone()
	if err == nil {
		_, err = clone.Funcs(baseFuncs(lang, theme)), error(nil)
	}
	if err != nil {
		log.Printf("render %s: %v", name, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := clone.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

func (s *server) renderPage(w http.ResponseWriter, r *http.Request, page string, data any) {
	s.render(w, r, s.pages[page], "layout", data)
}

// baseFuncs builds the template helpers for one language and theme.
func baseFuncs(lang, theme string) template.FuncMap {
	return template.FuncMap{
		"t":        func(key string) string { return translateIn(lang, key) },
		"tf":       func(format string, args ...any) string { return fmt.Sprintf(translateIn(lang, format), args...) },
		"appLang":  func() string { return lang },
		"appTheme": func() string { return theme },
		"safeHTML": func(v string) template.HTML { return template.HTML(v) },
		// the table of strings the browser composes for itself; every page
		// that runs any of our JavaScript needs it on <body data-strings>
		"strings": func() template.JS { return clientStrings(lang) },
	}
}

// prefs resolves the viewer's language and theme. Preferences live per
// account, so one person's dark mode is not everybody's.
func (s *server) prefs(r *http.Request) (string, string) {
	if u := userFrom(r); u.ID != 0 {
		st := s.st(r)
		return st.setting("lang", defaultLang), st.setting("theme", defaultTheme)
	}
	return defaultLang, defaultTheme
}

// st returns the data layer bound to the account behind this request.
func (s *server) st(r *http.Request) *store {
	return &store{db: s.db, uid: userFrom(r).ID}
}

func noCacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// fieldTypes are the metadata field kinds the browser can offer; the server
// only passes the list to the page, since it never sees a value.
var fieldTypes = []string{"text", "number", "date", "url", "checkbox"}

func writeJSONHeader(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
}

func writeJSON(w http.ResponseWriter, v any) {
	// no-store: these responses carry ciphertext and account facts, and no
	// cache between here and the browser has any business keeping either
	writeJSONHeader(w)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func httpError(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

const flashCookie = "ng_flash"

// flashError is for plain HTML form posts: instead of a bare error page it
// stores the message in a short-lived cookie and bounces back to the page,
// which renders it as a banner.
func (s *server) flashError(w http.ResponseWriter, r *http.Request, fallback, msg string) {
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(msg), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: isHTTPS(r),
	})
	http.Redirect(w, r, fallback, http.StatusSeeOther)
}

// takeFlash reads and clears the flash message, if any.
func takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
	msg, _ := url.QueryUnescape(c.Value)
	return msg
}

// ---- pages ----

type landingPage struct {
	Registration bool
	CSRF         string
}
