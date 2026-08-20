package main

import (
	"embed"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

//go:embed templates static
var assets embed.FS

func envOr(name, def string) string {
	if v := os.Getenv("NEUROSCRIBE_" + name); v != "" {
		return v
	}
	return def
}

func main() {
	dbPath := envOr("DB", "neuroscribe.db")
	addr := envOr("ADDR", "127.0.0.1:8484")

	// The subcommands run before the database is opened, deliberately. A
	// healthcheck is an HTTP probe and has no business touching SQLite — and
	// when it is run as root (a deploy script over ssh), merely opening the
	// database can leave root-owned WAL files behind that the service user
	// can never write again. Every fresh login then dies at the session
	// insert while existing sessions sail on, which is as confusing a
	// failure as this app can produce.
	// used as the container HEALTHCHECK, so the image ships no extra tools
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		target := addr
		if strings.HasPrefix(target, "0.0.0.0:") {
			target = "127.0.0.1:" + strings.TrimPrefix(target, "0.0.0.0:")
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + target + "/healthz")
		if err != nil {
			log.Fatalf("unhealthy: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			// the status and body are the diagnosis; swallowing them turns a
			// clear answer ("503 database unavailable") into a mystery
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			log.Fatalf("unhealthy: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return
	}
	// `neuroscribe mail test you@example.com` proves out SMTP settings
	if len(os.Args) > 1 && os.Args[1] == "mail" {
		runMailCLI(newMailer(envOr("BASE_URL", "http://"+addr)), os.Args[2:])
		return
	}
	// `neuroscribe bundle <dir>` writes the frozen frontend the Android app
	// carries. A build step, not a server one — it touches no database.
	if len(os.Args) > 1 && os.Args[1] == "bundle" {
		if err := runBundle(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}

	// Anything else is a mistake, and starting the server instead would look
	// like it had worked.
	if len(os.Args) > 1 {
		log.Fatalf("unknown subcommand %q: this binary takes only `healthcheck`, `mail` and `bundle`.\n"+
			"Accounts are managed with sqlite3 against %s — see the README.", os.Args[1], dbPath)
	}

	db := openDB(dbPath)
	defer db.Close()
	srv := newServer(db, pyodideDir(), typstDir(), addr)
	if srv.pyodideDir == "" {
		log.Printf("warning: no Python runtime in ./pyodide — run `make pyodide` to enable python snippets")
	} else {
		log.Printf("serving the browser's python runtime from %s", srv.pyodideDir)
	}
	if srv.typstDir == "" {
		log.Printf("warning: no typesetter in ./typst — run `make typst` to enable PDF export")
	} else {
		log.Printf("serving the browser's typesetter from %s", srv.typstDir)
	}
	switch {
	case srv.registrationOpen():
		log.Printf("public registration is OPEN")
		// every verification link is built from this URL, so a localhost value
		// with registration open means every mail sent is a dead link
		if strings.Contains(srv.mail.baseURL, "127.0.0.1") || strings.Contains(srv.mail.baseURL, "localhost") {
			log.Printf("warning: NEUROSCRIBE_BASE_URL is %q — emailed verification links will point at this machine, not your domain", srv.mail.baseURL)
		}
	case srv.mail.configured():
		log.Printf("mail is configured; set NEUROSCRIBE_REGISTRATION=open to allow public sign-ups")
	}
	var userCount int
	db.QueryRow("SELECT count(*) FROM users").Scan(&userCount)
	if userCount == 0 {
		if srv.registrationOpen() {
			log.Printf("no accounts yet — create the first one at %s/register", srv.mail.baseURL)
		} else {
			log.Printf("no accounts yet — configure mail and set NEUROSCRIBE_REGISTRATION=open to allow sign-up")
		}
	}
	log.Printf("Neuroscribe listening on http://%s", addr)
	log.Fatal(http.ListenAndServe(addr, srv))
}
