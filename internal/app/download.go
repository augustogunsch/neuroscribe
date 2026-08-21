package app

// Handing out the Android app.
//
// The app is not on a store, on purpose, so this is the only way to get it:
// a file on the server and a button that points at it. `make app-publish`
// puts both there — the APK and a line of text saying which version it is —
// and nothing else writes to that directory.
//
// It is deliberately separate from the rest of the deploy. Running `make
// deploy` updates the website and must leave every installed app exactly as it
// was; publishing a new APK is the one act that changes what people have on
// their phones, and it should take saying so.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const apkName = "neuroscribe.apk"

func downloadsDir() string {
	dir := envOr("DOWNLOADS", "downloads")
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// appRelease is what the landing page needs to know: whether there is an app
// to offer and what to call it.
type appRelease struct {
	Available bool
	Version   string
}

func (s *server) appRelease() appRelease {
	if s.downloadsDir == "" {
		return appRelease{}
	}
	if _, err := os.Stat(filepath.Join(s.downloadsDir, apkName)); err != nil {
		return appRelease{}
	}
	version := ""
	if raw, err := os.ReadFile(filepath.Join(s.downloadsDir, "version.txt")); err == nil {
		// A version is shown to people and put in a filename, so it may hold
		// digits and dots and nothing else; anything odd is simply not shown.
		candidate := strings.TrimSpace(string(raw))
		if len(candidate) <= 32 && candidate != "" &&
			strings.IndexFunc(candidate, func(r rune) bool {
				return !(r >= '0' && r <= '9') && r != '.'
			}) < 0 {
			version = candidate
		}
	}
	return appRelease{Available: true, Version: version}
}

// serveAPK hands over the file itself. One fixed name, resolved here rather
// than taken from the request, so the route cannot be talked into serving
// something else out of that directory or any other.
func (s *server) serveAPK(w http.ResponseWriter, r *http.Request) {
	if s.downloadsDir == "" {
		httpError(w, http.StatusNotFound, "no app has been published on this server")
		return
	}
	path := filepath.Join(s.downloadsDir, apkName)
	f, err := os.Open(path)
	if err != nil {
		httpError(w, http.StatusNotFound, "no app has been published on this server")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		httpError(w, http.StatusNotFound, "no app has been published on this server")
		return
	}
	name := apkName
	if v := s.appRelease().Version; v != "" {
		name = "neuroscribe-" + v + ".apk"
	}
	w.Header().Set("Content-Type", "application/vnd.android.package-archive")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// A published version never changes in place — a new one gets a new
	// version — so it is safe to keep, and it is a big file to re-fetch.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
