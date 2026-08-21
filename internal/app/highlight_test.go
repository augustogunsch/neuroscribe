package app

// The stylesheet has to name the classes the highlighter writes.
//
// It is the one place two vocabularies meet — highlight.js writes the markup,
// chroma supplies the palette — and a mismatch is invisible from either side:
// the code is highlighted, the CSS is served, and every token is the colour of
// ordinary text.

import (
	"regexp"
	"strings"
	"testing"
)

func TestTheHighlightPaletteNamesWhatTheHighlighterWrites(t *testing.T) {
	css := string(chromaCSS())

	// The scopes worth losing sleep over: a language with no keywords or
	// strings coloured is not highlighted in any sense a reader would notice.
	for _, scope := range []string{"keyword", "string", "comment", "number", "built_in", "title"} {
		if !strings.Contains(css, ".hljs-"+scope+"{") {
			t.Errorf("no rule for .hljs-%s; that token is left as plain text", scope)
		}
	}

	// Both themes, each fully scoped. Overlaying leaves gaps — a token only
	// one theme defines keeps the other's colour, which is how you get dark
	// text on a dark background for one token type and no idea why.
	for _, selector := range []string{
		`:root[data-theme="light"] .hljs-keyword{`,
		`:root[data-theme="dark"] .hljs-keyword{`,
		`:root[data-theme="auto"] .hljs-keyword{`,
	} {
		if !strings.Contains(css, selector) {
			t.Errorf("missing %q; that theme has no highlight colours", selector)
		}
	}
	if !strings.Contains(css, "@media (prefers-color-scheme: dark){") {
		t.Error("the automatic theme no longer follows the system")
	}

	// Light and dark must actually differ, or one of them is unreadable.
	rule := regexp.MustCompile(`:root\[data-theme="(light|dark)"\] \.hljs-keyword\{color:(#[0-9a-f]{6})`)
	found := map[string]string{}
	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		found[m[1]] = m[2]
	}
	if len(found) != 2 {
		t.Fatalf("expected a keyword colour per theme, got %v", found)
	}
	if found["light"] == found["dark"] {
		t.Errorf("both themes colour keywords %s; one of them is wrong", found["light"])
	}

	// The classes the client is allowed to keep have to cover what is styled
	// here, or the sanitizer strips exactly the spans this colours.
	render := readAsset(t, "static/render.js")
	if !strings.Contains(render, "NG_HLJS_CLASS") {
		t.Fatal("the sanitizer has no rule for highlight classes; every span is stripped")
	}
	// A span may carry a highlight class and nothing else.
	if !strings.Contains(render, `node.tagName === "SPAN" && NG_HLJS_CLASS.test(c)`) {
		t.Error("highlight classes are no longer allowed through on spans")
	}
	if !strings.Contains(render, `ALLOWED_TAGS: ["span"]`) {
		t.Error("the highlighter's output is no longer confined to spans")
	}
}
