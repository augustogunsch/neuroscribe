package main

// Syntax-highlight stylesheet generation.
//
// Notes are end-to-end encrypted, so the server cannot read them, let alone
// render them: the whole parse/sanitize/highlight pipeline runs in the browser
// (static/render.js). The server's only part in it is this stylesheet — the
// class names the client highlighter emits resolve against the chroma theme
// served at /static/chroma.css.

import (
	"bytes"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/styles"
)

var (
	chromaStyle     = styles.Get("github")
	chromaFormatter = chromahtml.New(chromahtml.WithClasses(true))
)

// chromaCSS returns the highlight stylesheet. The light and dark styles
// define different sets of token classes, so overlaying them leaves gaps
// (dark-on-dark tokens); instead each is fully scoped so exactly one style
// applies per theme state.
func chromaCSS() []byte {
	var lightBuf, darkBuf bytes.Buffer
	_ = chromaFormatter.WriteCSS(&lightBuf, chromaStyle)
	_ = chromaFormatter.WriteCSS(&darkBuf, styles.Get("github-dark"))
	light, dark := lightBuf.String(), darkBuf.String()

	scope := func(css, selector string) string {
		return strings.ReplaceAll(css, ".chroma", selector+" .chroma")
	}
	var out bytes.Buffer
	out.WriteString(scope(light, `:root[data-theme="light"]`))
	out.WriteString(scope(dark, `:root[data-theme="dark"]`))
	out.WriteString("@media (prefers-color-scheme: light){\n")
	out.WriteString(scope(light, `:root[data-theme="auto"]`))
	out.WriteString("}\n")
	out.WriteString("@media (prefers-color-scheme: dark){\n")
	out.WriteString(scope(dark, `:root[data-theme="auto"]`))
	out.WriteString("}\n")
	return out.Bytes()
}
