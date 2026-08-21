package app

// Assertions about the frontend, read from the shipped assets.
//
// Go tests over JavaScript files, which is unusual and deliberate: the
// properties they hold down — that the PIN never travels, that locking
// destroys the key, that the CSRF token is read from the cookie — are promises
// this project makes in prose, and prose does not fail a build.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readAsset reads from the embedded filesystem rather than from disk. Not for
// convenience — it is the difference between asserting something about a file
// that happens to be in the working directory and asserting it about the bytes
// this binary will actually serve.
func readAsset(t *testing.T, path string) string {
	t.Helper()
	b, err := assets.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// repoFile reaches the files that are not served to anyone and so are not
// embedded — the Makefile, the checksum manifest. Tests run with the package
// directory as their working directory, and this package is two levels down.
func repoFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", name))
	if err != nil {
		t.Fatalf("cannot read %s from the repository root: %v", name, err)
	}
	return b
}

// The PIN is a credential the server must never learn, and unlocking must work
// offline, so the PIN and the key it opens stay in the browser. The lock does
// reach the network — but only to confirm the account password when the lock is
// changed, never to unlock. These assert that boundary structurally.
func TestPinNeverLeavesTheBrowser(t *testing.T) {
	// Both halves, joined: the boundary is a property of the feature, not of a
	// file, and reading only one of them could be satisfied by moving a line
	// into the other.
	js := readAsset(t, "static/pin.js") + readAsset(t, "static/lock.js")

	// Every network call goes to one of these, and none of them carries the PIN:
	// two confirm the password (its salt, then its auth key) and one signs out.
	allowed := []string{"/auth/params", "/auth/verify", "/logout"}
	targets := regexp.MustCompile(`(?:fetch|ngPinPost)\("([^"]+)"`).FindAllStringSubmatch(js, -1)
	if len(targets) == 0 {
		t.Fatal("expected lock.js to contact the server for password confirmation")
	}
	for _, m := range targets {
		ok := false
		for _, p := range allowed {
			if strings.HasPrefix(m[1], p) {
				ok = true
				break
			}
		}
		if !ok {
			t.Errorf("lock.js contacts an unexpected endpoint %q — unlocking must stay local", m[1])
		}
	}
	// Unlocking opens the local record; it must never post the PIN to unlock.
	if !strings.Contains(js, "ngOpen(key, rec.wrapped)") {
		t.Error("unlocking no longer opens the local record")
	}
	if strings.Contains(js, "/pin/unlock") {
		t.Error("unlocking appears to go to the server — it must be local and offline")
	}
	// Confirmation sends the password's auth key, derived like login, not the PIN.
	if !strings.Contains(js, "password_auth") {
		t.Error("password confirmation no longer sends a password-derived key")
	}
	// What is persisted has to be the sealed key, never the PIN itself.
	if !strings.Contains(js, "wrapped: await ngSeal(key,") {
		t.Error("the stored record no longer holds a sealed key")
	}
	// The saved record is a fixed shape; the PIN is not one of its fields.
	record := js[strings.Index(js, "ngSavePinRecord({"):]
	record = record[:strings.Index(record, "});")]
	if strings.Contains(record, "pin") {
		t.Errorf("the PIN itself looks like it is being stored:\n%s", record)
	}
}

// Locking has to remove the key and the plaintext, not merely cover them: an
// overlay alone would leave both one devtools window away.
func TestLockDestroysTheKeyAndThePlaintext(t *testing.T) {
	js := readAsset(t, "static/lock.js")
	for _, required := range []string{
		"ngForgetDataKey()",  // out of sessionStorage
		"ngCachedKey = null", // out of memory
		"location.reload()",  // decrypted DOM replaced by ciphertext
		"ngStashDrafts",      // unsaved text sealed rather than dropped
	} {
		if !strings.Contains(js, required) {
			t.Errorf("ngLockNow no longer does %s", required)
		}
	}
	// A draft is sealed before it is cleared, so locking never leaves typed
	// text sitting in storage in the clear.
	if !strings.Contains(js, "blob: await ngSeal(key, value)") {
		t.Error("unsaved drafts are no longer sealed before the key is discarded")
	}
}

// Six digits only survives because guessing is made expensive and finite.
func TestPinGuessingIsBounded(t *testing.T) {
	lock := readAsset(t, "static/pin.js")
	if !strings.Contains(lock, "NG_PIN_MAX_TRIES") || !strings.Contains(lock, "ngClearPin()") {
		t.Error("wrong PINs are no longer counted towards erasing the record")
	}
	crypto := readAsset(t, "static/crypto.js")
	if !strings.Contains(crypto, "NG_PIN_ITERATIONS = 3000000") {
		t.Error("the PIN KDF cost changed; if that is deliberate, update this test")
	}
	// The record carries its own iteration count, so raising the constant
	// cannot lock anyone out of a PIN they already set.
	if !strings.Contains(crypto, "iterations || NG_PIN_ITERATIONS") {
		t.Error("ngPinKey ignores the iteration count stored with the record")
	}
	if !strings.Contains(lock, "ngPinKey(pin, rec.salt, rec.iter)") {
		t.Error("unlocking no longer uses the record's own iteration count")
	}
}

// The password is the only thing protecting the data, and the server cannot
// check it. The rule must stay a strength estimate rather than a character
// recipe: composition rules push people towards short, guessable passwords.
func TestPasswordPolicyMeasuresStrength(t *testing.T) {
	js := readAsset(t, "static/strength.js")
	if !strings.Contains(js, "NG_MIN_SCORE = 4") {
		t.Error("the accepted zxcvbn score changed; if that is deliberate, update this test")
	}
	if !strings.Contains(js, "NG_MIN_PASSWORD = 12") {
		t.Error("the minimum length changed; if that is deliberate, update this test")
	}
	// Character-class tests are the shape a composition rule takes. (The word
	// "symbol" appears in the file explaining why there is no such rule.)
	for _, recipe := range []string{"[A-Z]", "[a-z]", "[0-9]", "[!@#$"} {
		if strings.Contains(js, recipe) {
			t.Errorf("strength.js looks like it enforces composition rules (%q)", recipe)
		}
	}
	// Registration must not be able to proceed on the meter alone.
	e2e := readAsset(t, "static/e2e.js")
	if !strings.Contains(e2e, "await ngRatePassword(password") || !strings.Contains(e2e, "if (!rating.ok)") {
		t.Error("the register form no longer gates submission on the strength estimate")
	}
}

// Both features depend on being loaded at all, and on the page saying who is
// signed in so a shared browser never offers one account another's lock.
func TestLockAndStrengthAreWiredIn(t *testing.T) {
	ts, ck, _ := newTestServer(t)

	app := bodyOf(t, doGet(t, ts, ck, "/"))
	for _, want := range []string{"/static/pin.js", "/static/lock.js", "/static/strings.js", "data-user="} {
		if !strings.Contains(app, want) {
			t.Errorf("app layout is missing %s", want)
		}
	}

	// /register answers 404 unless mail is configured and sign-up is opened,
	// so the page itself is checked as a template rather than over HTTP.
	reg := readAsset(t, "templates/register.html")
	for _, want := range []string{"/static/strength.js", "data-pw-meter", "data-strings="} {
		if !strings.Contains(reg, want) {
			t.Errorf("register page is missing %s", want)
		}
	}
	// A 72-character cap is bcrypt's input limit leaking into the UI. The
	// server hashes a fixed-length derived key, never the password, so a cap
	// that blocks long passphrases has no reason to exist here.
	if strings.Contains(reg, `maxlength="72"`) {
		t.Error("the password field carries a 72-character limit that would block passphrases")
	}

	if r := doGet(t, ts, ck, "/static/vendor/zxcvbn.min.js"); r.StatusCode != 200 {
		t.Fatalf("the strength estimator is not vendored: %d", r.StatusCode)
	}
}

// The shell is one cached document for every page, so a CSRF token read from
// its markup goes stale. Every client-side reader must use the cookie.
func TestCSRFTokenIsReadFromTheCookie(t *testing.T) {
	for _, asset := range []string{
		"static/csrf.js", "static/pin.js", "static/lock.js", "static/app.js",
		"static/sync.js", "static/settings.js", "static/router.js",
	} {
		src := readAsset(t, asset)
		if strings.Contains(src, `meta[name="csrf-token"]`) {
			t.Errorf("%s still reads the CSRF token from a meta tag; the cached shell makes that stale", asset)
		}
	}
	if !strings.Contains(readAsset(t, "static/csrf.js"), "ng_csrf=") {
		t.Error("csrf.js no longer reads the token from the cookie")
	}
}

// The index is one column at one width.
//
// It is the app's manual and its front door, and it is assembled from parts —
// an intro, a list of instructions, a copyable example, a list of recent notes
// — that have no reason to agree about width unless something makes them. When
// only the prose was bounded, the note list ran to the full width of the
// content area beside text that stopped well short of it.
//
// Layout is what these tests cannot see: they read the source without laying it
// out. So this checks the structure that produces the layout, and
// scripts/index-probe.html measures the result.
func TestTheIndexIsOneColumn(t *testing.T) {
	views := readAsset(t, "static/views.js")
	if !strings.Contains(views, `ngEl("div", { class: "index" }`) {
		t.Fatal("the index is no longer wrapped in a single column")
	}
	// Everything the view renders goes inside it, or the part left out is the
	// part that looks wrong.
	at := strings.Index(views, `host.replaceChildren(ngEl("div", { class: "index" }`)
	if at < 0 {
		t.Fatal("the column is built but not what the view renders")
	}
	for _, part := range []string{"hero", "toc-title", "recent"} {
		if !strings.Contains(views[at:at+400], part) {
			t.Errorf("%q is not inside the column", part)
		}
	}

	css := string(repoFile(t, "web/static/style.css"))
	if !strings.Contains(css, ".index { max-width:") {
		t.Error("the column has no width, so its contents each take their own")
	}
	// One source for the measure. Two elements carrying their own max-width is
	// how they came to disagree in the first place.
	if strings.Contains(css, ".hero { max-width:") {
		t.Error(".hero sets its own width again; the column's width is the only one")
	}
}
