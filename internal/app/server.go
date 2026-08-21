package app

// The server: what it holds, and every address it answers.
//
// One file for the route table on purpose. The set of addresses an app answers
// is the shortest true description of what it does, and it is the first thing
// anyone reading this will want; scattering it across the files that implement
// each route would mean reconstructing it by grep every time.

import (
	"database/sql"
	"html/template"
	"net/http"
)

const (
	maxTitleLen = 200
)

type server struct {
	db            *sql.DB
	pyodideDir    string
	downloadsDir  string
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
		downloadsDir:     downloadsDir(),
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
		files := []string{"templates/" + page}
		// The landing page's figure is inlined rather than linked. It has to
		// be part of this document to take its colour from it — an <img> is
		// its own document and inherits nothing — and it cannot be a mask,
		// which would flatten the one deliberate colour in it to the same
		// tone as the axes. Drawn by scripts/make-landing-figure.html.
		if page == "landing.html" {
			files = append(files, "static/generated/landing-figure.svg")
		}
		s.pages[page] = template.Must(template.New("").Funcs(fm).ParseFS(assets, files...))
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
	m.HandleFunc("GET /download/neuroscribe.apk", s.serveAPK)
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
	// erases the account and everything in it, password proven first
	m.HandleFunc("POST /account/delete", s.deleteAccount)

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
