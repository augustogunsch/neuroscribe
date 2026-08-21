package app

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The bundle is the app. A file the shell asks for and the bundle does not
// hold is not a build error and not a test failure anywhere else — it is a
// blank screen on a phone, discovered by whoever installed it, on a version
// that cannot be fixed without publishing another one. So it is checked here.
func TestBundleHoldsEverythingTheShellAsksFor(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "web")
	if err := runBundle([]string{out}); err != nil {
		t.Fatalf("bundling: %v", err)
	}

	shell, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatalf("no shell in the bundle: %v", err)
	}

	// Every same-origin URL the shell names: scripts, styles, icons, manifest.
	refs := regexp.MustCompile(`(?:src|href)="(/[^"]+)"`).FindAllStringSubmatch(string(shell), -1)
	if len(refs) < 5 {
		t.Fatalf("only %d references found in the shell; the pattern has stopped matching", len(refs))
	}
	checked := 0
	for _, m := range refs {
		url := m[1]
		// Only files. The shell also links to its own addresses — /settings,
		// /trash — which are rendered in the browser and are not on disk here.
		if strings.HasPrefix(url, "//") || !strings.Contains(filepath.Base(url), ".") {
			continue
		}
		checked++
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(strings.TrimPrefix(url, "/")))); err != nil {
			t.Errorf("the shell loads %s, which the bundle does not contain", url)
		}
	}
	if checked < 5 {
		t.Errorf("only %d files checked; the shell's references are no longer being found", checked)
	}

	// Fetched by name at runtime rather than named in the shell, so the loop
	// above cannot see them. chroma.css is generated and has caught this out
	// once already; the worker and the sandboxed runner are loaded on demand.
	for _, needed := range []string{
		"static/chroma.css",
		"static/typst-worker.js",
		"static/runner.html",
		"strings/en.json",
		"strings/pt-BR.json",
	} {
		if _, err := os.Stat(filepath.Join(out, filepath.FromSlash(needed))); err != nil {
			t.Errorf("the bundle is missing %s, which the app fetches at runtime", needed)
		}
	}
}

// The whole point of the bundle: it must carry no account in it, and it must
// tell the frontend not to install a service worker — a worker would fetch the
// shell from the server again and quietly undo the freeze.
func TestBundledShellIsAccountLessAndWorkerFree(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "web")
	if err := runBundle([]string{out}); err != nil {
		t.Fatalf("bundling: %v", err)
	}
	shell, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(shell)
	if !strings.Contains(body, `data-native="1"`) {
		t.Error("the bundled shell is not marked native, so the app would register a service worker")
	}
	if !strings.Contains(body, `data-user=""`) {
		t.Error("the bundled shell carries a username; it is built once for everyone")
	}
	// boot.js is what reads the marker; if that link is broken the marker is
	// decoration and the freeze is not enforced anywhere.
	boot, err := os.ReadFile(filepath.Join(out, "static", "boot.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(boot), "dataset.native") {
		t.Error("boot.js no longer checks the native marker before registering the worker")
	}
}
