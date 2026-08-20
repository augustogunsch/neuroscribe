package main

// Freezing the frontend, so an installed app stays the app that was installed.
//
// The web app updates itself: deploy a new build and every browser picks it up
// on the next reload. That is right for a page and wrong for something a person
// installed on their phone, which should change when they say so and not when
// the server does. So the Android app carries its own copy of the frontend and
// `neuroscribe bundle` is what writes that copy.
//
// What lands here is everything the browser is served *about the app*: the
// shell, the scripts, the stylesheet, the fonts, the language tables. What does
// not is everything about the account — /sync, /login, /account — which the app
// keeps fetching from the server over the network, because a note written on
// the laptop has to reach the phone whatever version the phone is running.
//
// The shell is rendered with no account in it. On the web the server stamps the
// signed-in name into the page; here there is no server and no request, so the
// page comes out blank and the app fills it in from /account on first run. See
// ngHydrateShell in static/router.js.

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// runBundle writes the frozen frontend into dir, replacing whatever is there.
func runBundle(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: neuroscribe bundle <directory>")
	}
	dir := args[0]
	if dir == "/" || dir == "." || strings.TrimSpace(dir) == "" {
		return fmt.Errorf("refusing to bundle into %q", dir)
	}
	// Replacing rather than merging: a file dropped from the build has to
	// disappear from the bundle too, or the app keeps loading a script the
	// rest of the frontend no longer agrees with.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing %s: %w", dir, err)
	}

	for _, f := range []struct {
		path  string
		write func() ([]byte, error)
	}{
		{"index.html", bundledShell},
		{"strings/en.json", func() ([]byte, error) { return bundledStrings("en") }},
		{"strings/pt-BR.json", func() ([]byte, error) { return bundledStrings("pt-BR") }},
		{"manifest.webmanifest", bundledManifest},
		// Built from the highlighter's own theme rather than kept as a file, so
		// it is not among the embedded assets and has to be asked for by name.
		// Miss it and the app fetches its syntax colours over the network,
		// which is a strange thing for an offline app to need.
		{"static/chroma.css", func() ([]byte, error) { return chromaCSS(), nil }},
	} {
		body, err := f.write()
		if err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
		if err := writeUnder(dir, f.path, body); err != nil {
			return err
		}
	}

	// Every embedded asset, at the path the app asks for it by. The runtimes
	// (pyodide, typst) are deliberately absent: they are 149 MB of optional
	// extras that stay on the server and are fetched when they are used.
	count := 0
	err := fs.WalkDir(assets, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := assets.ReadFile(path)
		if err != nil {
			return err
		}
		count++
		return writeUnder(dir, path, body)
	})
	if err != nil {
		return fmt.Errorf("copying assets: %w", err)
	}
	fmt.Printf("bundled the shell and %d assets into %s\n", count, dir)
	return nil
}

// writeUnder refuses to escape the destination, so a bad asset name cannot
// scatter files across the filesystem of whoever runs the build.
func writeUnder(dir, name string, body []byte) error {
	clean := filepath.Clean(filepath.Join(dir, filepath.FromSlash(name)))
	root := filepath.Clean(dir)
	if clean != root && !strings.HasPrefix(clean, root+string(os.PathSeparator)) {
		return fmt.Errorf("asset %q escapes the bundle directory", name)
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return err
	}
	return os.WriteFile(clean, body, 0o644)
}

// bundledShell renders app.html with nothing account-shaped in it.
func bundledShell() ([]byte, error) {
	tmpl, err := template.New("").Funcs(baseFuncs(defaultLang, defaultTheme)).
		ParseFS(assets, "templates/app.html")
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	// No username: this shell is handed to whoever installs the app, and the
	// account it ends up signed into is not known when it is built. No runtime
	// flags either — whether the server has Python and the typesetter is a fact
	// about the server, asked for at runtime rather than frozen in.
	if err := tmpl.ExecuteTemplate(&out, "layout", appShell{}); err != nil {
		return nil, err
	}
	body := out.String()
	// The marker the frontend reads to know it is running from a bundle: no
	// service worker (there is nothing to cache that is not already local, and
	// a worker would re-fetch the shell from the server and undo the freeze),
	// and the account details come from /account instead of from the page.
	body = strings.Replace(body, `<body data-app="1"`, `<body data-app="1" data-native="1"`, 1)
	if !strings.Contains(body, `data-native="1"`) {
		return nil, fmt.Errorf("app.html no longer starts its body tag the way the bundler expects")
	}
	return []byte(body), nil
}

func bundledStrings(lang string) ([]byte, error) {
	table := translations[lang]
	if table == nil {
		table = map[string]string{}
	}
	return json.Marshal(table)
}

func bundledManifest() ([]byte, error) {
	return json.Marshal(map[string]any{
		"name": "Neuroscribe", "short_name": "Neuroscribe",
		"start_url": "/", "scope": "/", "display": "standalone",
		"background_color": "#17171c", "theme_color": "#17171c",
	})
}
