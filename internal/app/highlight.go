package app

// Syntax-highlight stylesheet generation.
//
// Notes are end-to-end encrypted, so the server cannot read them, let alone
// render them: the whole parse/sanitize/highlight pipeline runs in the browser
// (static/render.js, with highlight.js). The server's only part in it is this
// stylesheet.
//
// The colours come from chroma rather than from a highlight.js theme, because
// chroma ships github and github-dark as data this can read token by token —
// which is what lets one file carry both themes, each fully scoped, instead of
// two stylesheets fighting over the same selectors. The names are highlight.js's
// because highlight.js is what writes the markup.
//
// The pairing below is the seam between those two vocabularies, and it is the
// only place they meet. A scope highlight.js emits that is not named here is
// simply not coloured, which is the right failure: unstyled code is readable.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
)

var chromaStyle = styles.Get("github")

// What highlight.js calls a thing, and what chroma calls the same thing.
//
// Only scopes highlight.js actually emits, and only ones chroma has an opinion
// about. Several collapse: chroma does not distinguish a selector's class from
// any other class name, and neither does any theme worth reading.
var hljsScopes = map[string]chroma.TokenType{
	"keyword":           chroma.Keyword,
	"built_in":          chroma.NameBuiltin,
	"type":              chroma.KeywordType,
	"literal":           chroma.KeywordConstant,
	"number":            chroma.LiteralNumber,
	"operator":          chroma.Operator,
	"punctuation":       chroma.Punctuation,
	"property":          chroma.NameAttribute,
	"regexp":            chroma.LiteralStringRegex,
	"string":            chroma.LiteralString,
	"char":              chroma.LiteralStringChar,
	"subst":             chroma.LiteralStringInterpol,
	"symbol":            chroma.LiteralStringSymbol,
	"class":             chroma.NameClass,
	"function":          chroma.NameFunction,
	"variable":          chroma.NameVariable,
	"title":             chroma.NameFunction,
	"params":            chroma.Name,
	"comment":           chroma.Comment,
	"doctag":            chroma.LiteralStringDoc,
	"meta":              chroma.CommentPreproc,
	"section":           chroma.GenericHeading,
	"tag":               chroma.NameTag,
	"name":              chroma.NameTag,
	"attr":              chroma.NameAttribute,
	"attribute":         chroma.NameAttribute,
	"bullet":            chroma.Keyword,
	"quote":             chroma.Comment,
	"link":              chroma.NameAttribute,
	"emphasis":          chroma.GenericEmph,
	"strong":            chroma.GenericStrong,
	"addition":          chroma.GenericInserted,
	"deletion":          chroma.GenericDeleted,
	"selector-tag":      chroma.NameTag,
	"selector-id":       chroma.NameDecorator,
	"selector-class":    chroma.NameClass,
	"selector-attr":     chroma.NameAttribute,
	"selector-pseudo":   chroma.NameDecorator,
	"template-tag":      chroma.CommentPreproc,
	"template-variable": chroma.NameVariable,
}

// rules renders one theme as declarations for highlight.js's class names.
func rules(style *chroma.Style) string {
	names := make([]string, 0, len(hljsScopes))
	for name := range hljsScopes {
		names = append(names, name)
	}
	sort.Strings(names) // a generated file that reorders itself is a bad diff

	var out bytes.Buffer
	for _, name := range names {
		entry := style.Get(hljsScopes[name])
		var decls []string
		if entry.Colour.IsSet() {
			decls = append(decls, "color:"+entry.Colour.String())
		}
		if entry.Bold == chroma.Yes {
			decls = append(decls, "font-weight:bold")
		}
		if entry.Italic == chroma.Yes {
			decls = append(decls, "font-style:italic")
		}
		// Structure rather than palette: emphasis is slanted and strong is
		// heavy in any theme, and chroma's github pair says nothing about
		// either, so neither would show at all in Markdown inside a fence.
		switch name {
		case "emphasis":
			decls = append(decls, "font-style:italic")
		case "strong":
			decls = append(decls, "font-weight:bold")
		}
		if len(decls) == 0 {
			continue
		}
		// highlight.js writes compound scopes as several classes —
		// class="hljs-title function_" — so matching one class is enough.
		fmt.Fprintf(&out, ".hljs-%s{%s}\n", name, strings.Join(decls, ";"))
	}
	return out.String()
}

// chromaCSS returns the highlight stylesheet.
//
// Each theme is fully scoped rather than overlaid. Overlaying leaves gaps: the
// two themes do not define exactly the same set of tokens, so a token coloured
// by only one of them keeps the other's colour and you get dark text on a dark
// background for that one token type.
func chromaCSS() []byte {
	scope := func(css, selector string) string {
		var out bytes.Buffer
		for _, line := range strings.Split(strings.TrimSpace(css), "\n") {
			if line == "" {
				continue
			}
			out.WriteString(selector + " " + line + "\n")
		}
		return out.String()
	}

	light, dark := rules(chromaStyle), rules(styles.Get("github-dark"))

	var out bytes.Buffer
	out.WriteString("/* generated: see internal/app/highlight.go */\n")
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
