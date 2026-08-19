package main

// The app shell, and the few facts the browser cannot work out alone.
//
// Every address inside the app returns the same document. That is what makes
// the app work offline: a service worker can cache one page and satisfy every
// navigation from it, including notes written on a train and addresses that
// have never been requested from this server at all. The page carries no
// content — it carries a session, a CSRF token, and the answer to "does this
// server have the Python runtime and the typesetter".

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
)

// appShell answers every navigation the app makes.
func (s *server) appShell(w http.ResponseWriter, r *http.Request) {
	// A signed-out visitor at the root gets the landing page; anywhere else
	// requireAuth has already sent them to sign in.
	if s.currentUser(r) == "" {
		s.renderPage(w, r, "landing.html", landingPage{
			Registration: s.registrationOpen(),
			CSRF:         csrfFromContext(r),
		})
		return
	}
	// The shell must never be served stale after an upgrade — it names the
	// asset versions the rest of the app is loaded from.
	w.Header().Set("Cache-Control", "no-cache")
	// The marker the service worker caches by. Without it the worker would
	// cache whatever "/" returns — including the signed-out landing page —
	// and then serve that for every app navigation: a blank app that no
	// amount of reloading fixes, because the poisoned copy answers first.
	w.Header().Set("X-NG-Shell", "1")
	s.renderPage(w, r, "app.html", s.shell(r))
}

type accountResponse struct {
	Username        string `json:"username"`
	Plan            string `json:"plan"`
	Notes           int    `json:"notes"`
	Images          int    `json:"images"`
	MaxNotes        int    `json:"max_notes"`
	MaxImages       int    `json:"max_images"`
	ImageCap        string `json:"image_cap"`
	NoteCap         string `json:"note_cap"`
	MaxChapterBytes int    `json:"max_chapter_bytes"`
	RunnerOK        bool   `json:"runner_ok"`
	PDFOK           bool   `json:"pdf_ok"`
	Lang            string `json:"lang"`
	Theme           string `json:"theme"`
}

// accountInfo is what the settings page shows about the account itself. It is
// the one screen that genuinely needs the server, and the one screen that is
// allowed to be unavailable offline.
func (s *server) accountInfo(w http.ResponseWriter, r *http.Request) {
	st := s.st(r)
	u := st.usage()
	lang, theme := s.prefs(r)
	writeJSON(w, accountResponse{
		Username:        s.currentUser(r),
		Plan:            u.Plan.Name,
		Notes:           u.Notes,
		Images:          u.Images,
		MaxNotes:        u.Plan.MaxNotes,
		MaxImages:       u.Plan.MaxImages,
		ImageCap:        u.ImageCap,
		NoteCap:         u.NoteCap,
		MaxChapterBytes: u.Plan.MaxChapterBytes,
		RunnerOK:        s.pyodideDir != "",
		PDFOK:           s.typstDir != "",
		Lang:            lang,
		Theme:           theme,
	})
}

// savePrefs stores language and colour scheme. The browser keeps its own copy
// and applies it immediately; this is only so a second device agrees.
func (s *server) savePrefs(w http.ResponseWriter, r *http.Request) {
	st := s.st(r)
	if lang := r.FormValue("lang"); validLang(lang) {
		st.setSetting("lang", lang)
	}
	if theme := r.FormValue("theme"); validTheme(theme) {
		st.setSetting("theme", theme)
	}
	w.WriteHeader(http.StatusNoContent)
}

// serveStrings hands over a whole language. Pages are built in the browser,
// so there is no per-page rendering step for a template to translate in — the
// whole table goes to the browser instead, where it is cached with everything
// else and works with the network off.
func (s *server) serveStrings(w http.ResponseWriter, r *http.Request) {
	lang := strings.TrimSuffix(r.PathValue("lang"), ".json")
	if !validLang(lang) {
		httpError(w, 404, "no such language")
		return
	}
	table := translations[lang]
	if table == nil {
		table = map[string]string{} // English is the keys themselves
	}
	blob, err := json.Marshal(table)
	if err != nil {
		httpError(w, 500, "could not encode the language")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// Versioned by the build, and the service worker revalidates on upgrade.
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(blob)
}

// serveManifest describes the installable app. It is generated rather than
// static so the name and colours follow the same source as the pages.
func (s *server) serveManifest(w http.ResponseWriter, r *http.Request) {
	manifest := map[string]any{
		"name":             "Neuroscribe",
		"short_name":       "Neuroscribe",
		"description":      "An encrypted knowledge base that works offline.",
		"start_url":        "/",
		"scope":            "/",
		"display":          "standalone",
		"background_color": "#17171c",
		"theme_color":      "#17171c",
		// One scalable icon rather than a set of sizes: the logo is already an
		// SVG, and listing PNGs that do not exist would leave the installer
		// fetching 404s.
		"icons": []map[string]any{
			{"src": "/static/logo.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "any"},
			{"src": "/static/logo.svg", "sizes": "any", "type": "image/svg+xml", "purpose": "maskable"},
		},
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(manifest)
}

// serveServiceWorker hands out static/sw.js from the root.
//
// A worker may only control pages at or below its own path, so one served
// from /static/ could never take charge of /notes/… — which is every page
// that has to work offline. Hence this: same file, root scope.
func (s *server) serveServiceWorker(w http.ResponseWriter, r *http.Request) {
	body, err := assets.ReadFile("static/sw.js")
	if err != nil {
		httpError(w, 500, "service worker missing")
		return
	}
	// The cache names carry a version, and a worker only reinstalls when its
	// own bytes change. Deriving that version from the build means adding a
	// file to the precache list is enough on its own — with a hand-bumped
	// constant, forgetting the bump would leave the new file uncached, a
	// mistake that only ever shows up offline.
	body = []byte(strings.Replace(string(body), `const NG_VERSION = "v1";`,
		"const NG_VERSION = \""+s.assetVersion+"\";", 1))
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// Never cached: this file decides what everything else caches.
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	w.Write(body)
}

// buildVersion is a fingerprint of everything the browser is served, so a new
// build invalidates the caches that hold the old one.
func buildVersion() string {
	sum := sha256.New()
	fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		body, err := assets.ReadFile(path)
		if err != nil {
			return nil
		}
		sum.Write([]byte(path))
		sum.Write(body)
		return nil
	})
	return hex.EncodeToString(sum.Sum(nil))[:12]
}
