package main

// Turning an answer into a response: templates, JSON, errors, flashes.
//
// Very little is rendered on the server any more — the app builds its own
// pages in the browser — so what is left is the shell, the two pages that
// exist before there is an account, and the small helpers every handler uses
// to say "here is some JSON" or "that went wrong".

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
)

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
	App          appRelease
}
