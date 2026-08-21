package app

// Minimal i18n: templates call {{t "English text"}} / {{tf "format %s" args}};
// the English string is the key, unknown keys fall back to themselves. The
// language is an app-wide setting (single-user app), held atomically so a
// settings change applies without restart.

import (
	"encoding/json"
	"fmt"
	"html/template"
)

var languages = []struct{ Code, Name string }{
	{"en", "English"},
	{"pt-BR", "Português (Brasil)"},
}

func validLang(code string) bool {
	for _, l := range languages {
		if l.Code == code {
			return true
		}
	}
	return false
}

var themes = []string{"auto", "light", "dark"}

func validTheme(t string) bool {
	for _, v := range themes {
		if v == t {
			return true
		}
	}
	return false
}

const (
	defaultLang  = "en"
	defaultTheme = "dark"
)

// translateIn looks a string up in one language; unknown keys fall back to
// the English text, which is the key itself.
func translateIn(lang, key string) string {
	if m, ok := translations[lang]; ok {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return key
}

// translate uses the language of the account behind the request; signed-out
// pages get the default.
func (s *server) translate(key string) string {
	return translateIn(defaultLang, key)
}

func (s *server) translatef(format string, args ...any) string {
	return fmt.Sprintf(s.translate(format), args...)
}

// clientStrings hands the browser the few strings it composes on its own —
// snippet results, which the server never sees and so cannot render. Anything
// not listed here falls back to the English key in ngT().
var clientKeys = []string{
	"Run", "Running…", "Starting Python…", "Loading libraries…",
	"timed out", "error", "(no output)",
	// the typeset PDF is assembled in the browser too
	"Contents",
	// password strength
	"Too short", "Use at least 12 characters, a few unrelated words is the easiest way.",
	"Could not check strength",
	"The strength estimator did not load. A long passphrase is still your best move.",
	"Very weak", "Weak", "Fair", "Strong", "Very strong",
	"Guessing this offline would take about ", "Close. One more word would put this out of reach.",
	// the lock screen and its settings
	"Locked", "Enter your PIN to unlock this device.", "PIN", "Unlock", "Unlocking…",
	"The PIN is six digits.", "Wrong PIN. ", "Attempts left: ",
	"Forgot your PIN? Sign in with your password",
	"Unlock the app before setting a PIN.", "The two PINs do not match.",
	"set on this device", "not set on this device", "Set PIN", "Change PIN",
}

func clientStrings(lang string) template.JS {
	out := make(map[string]string, len(clientKeys))
	for _, key := range clientKeys {
		out[key] = translateIn(lang, key)
	}
	// into a data attribute, so it needs to survive HTML escaping intact
	blob, err := json.Marshal(out)
	if err != nil {
		return template.JS("{}")
	}
	return template.JS(blob)
}
