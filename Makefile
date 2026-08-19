BINARY  := neuroscribe
GO      ?= go

.PHONY: all build release run run-open mail-dev pyodide typst vendor assets test vet fmt check hooks clean

all: build

build:
	$(GO) build -o $(BINARY) .

# The binary is static (CGO off), so the right way to deploy to a small server
# is to build here and copy the result: the pure-Go SQLite alone costs minutes
# of compile time on one slow vCPU, and can swap a 1 GB machine into the
# ground. `make release` produces dist/neuroscribe-linux-amd64 in seconds on a
# laptop; scp it and skip compiling on the server entirely.
RELEASE_GOOS   ?= linux
RELEASE_GOARCH ?= amd64
release:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=$(RELEASE_GOOS) GOARCH=$(RELEASE_GOARCH) \
		$(GO) build -trimpath -ldflags="-s -w" \
		-o dist/$(BINARY)-$(RELEASE_GOOS)-$(RELEASE_GOARCH) .
	@ls -lh dist/$(BINARY)-$(RELEASE_GOOS)-$(RELEASE_GOARCH) | awk '{print $$5, $$9}'

# .env.local (untracked) is sourced when present — see .env.example
run: build
	@if [ -f .env.local ]; then set -a; . ./.env.local; set +a; fi; ./$(BINARY)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

# full quality gate (same as the pre-commit hook)
check: vendor
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then \
		echo "gofmt required for:"; echo "$$unformatted"; exit 1; fi
	$(GO) vet ./...
	$(GO) test ./...

# install the git pre-commit hook
hooks:
	git config core.hooksPath .githooks

# Run locally with public sign-up enabled, delivering verification mail to a
# local Mailpit catcher (make mail-dev) — no provider account needed.
run-open: build
	NEUROSCRIBE_SMTP_HOST=127.0.0.1 \
	NEUROSCRIBE_SMTP_PORT=1025 \
	NEUROSCRIBE_MAIL_FROM='Neuroscribe <noreply@localhost>' \
	NEUROSCRIBE_BASE_URL=http://127.0.0.1:8484 \
	NEUROSCRIBE_REGISTRATION=open \
	./$(BINARY)

# Both runtimes below are fetched through scripts/fetch.sh, which installs
# nothing whose SHA-256 is missing from assets.sha256. They are neither built
# here nor committed, so that file is the whole of the trust: see its header.
FETCH := scripts/fetch.sh

# Python and JavaScript snippets run in the browser, so the runtime is served
# from here rather than executed on the server. ~116 MB, fetched on demand and
# never committed.
PYODIDE_VERSION := 0.26.4
PYODIDE_BASE := https://cdn.jsdelivr.net/pyodide/v$(PYODIDE_VERSION)/full
PYODIDE_FILES := pyodide.js pyodide.asm.js pyodide.asm.wasm python_stdlib.zip \
	pyodide-lock.json \
	numpy-1.26.4-cp312-cp312-pyodide_2024_0_wasm32.whl \
	scipy-1.12.0-cp312-cp312-pyodide_2024_0_wasm32.whl \
	pandas-2.2.0-cp312-cp312-pyodide_2024_0_wasm32.whl \
	sympy-1.12-py3-none-any.whl mpmath-1.3.0-py3-none-any.whl \
	openblas-0.3.26.zip six-1.16.0-py2.py3-none-any.whl \
	pytz-2024.1-py2.py3-none-any.whl \
	python_dateutil-2.9.0.post0-py2.py3-none-any.whl

pyodide:
	@for f in $(PYODIDE_FILES); do \
		$(FETCH) $(PYODIDE_BASE)/$$f pyodide/$$f || exit 1; \
	done
	@echo "pyodide ready: $$(du -sh pyodide | cut -f1)"

# Typesetting also happens in the browser, so that a note never leaves it to be
# turned into a PDF. Typst replaces pdflatex here: it compiles to WebAssembly,
# which LaTeX does not. ~34 MB, fetched on demand and never committed.
TYPST_VERSION  := 0.7.0
TYPST_NPM      := https://registry.npmjs.org/@myriaddreamin
TYPST_ASSETS   := https://raw.githubusercontent.com/typst/typst-assets/main/files/fonts
MITEX_VERSION  := 0.2.5

# New Computer Modern is the classic TeX face, so exported PDFs read like
# LaTeX documents. The math font is what makes formulas work at all — Typst
# needs a real OpenType MATH table.
TYPST_FONTS := NewCM10-Regular.otf NewCM10-Bold.otf NewCM10-Italic.otf \
	NewCM10-BoldItalic.otf NewCMMath-Regular.otf \
	DejaVuSansMono.ttf DejaVuSansMono-Bold.ttf

# The archives are checked before tar sees them: an unpacker is a poor place
# to discover that a download was not what it claimed to be.
typst:
	@mkdir -p typst/fonts typst/packages
	@if [ ! -f typst/typst_ts_web_compiler_bg.wasm ]; then \
		$(FETCH) $(TYPST_NPM)/typst-ts-web-compiler/-/typst-ts-web-compiler-$(TYPST_VERSION).tgz \
			typst/dl/typst-ts-web-compiler-$(TYPST_VERSION).tgz || exit 1; \
		tar xzf typst/dl/typst-ts-web-compiler-$(TYPST_VERSION).tgz -C typst --strip-components=2 \
			package/pkg/typst_ts_web_compiler_bg.wasm package/pkg/typst_ts_web_compiler.mjs; \
	fi
	@if [ ! -f typst/typst.mjs ]; then \
		$(FETCH) $(TYPST_NPM)/typst.ts/-/typst.ts-$(TYPST_VERSION).tgz \
			typst/dl/typst.ts-$(TYPST_VERSION).tgz || exit 1; \
		tar xzf typst/dl/typst.ts-$(TYPST_VERSION).tgz -C typst --strip-components=3 package/dist/esm; \
		mv typst/index.mjs typst/typst.mjs; \
		rm -f typst/*.d.mts typst/main.bundle.js; \
	fi
	@for f in $(TYPST_FONTS); do \
		$(FETCH) $(TYPST_ASSETS)/$$f typst/fonts/$$f || exit 1; \
	done
	@if [ ! -f typst/packages/mitex/lib.typ ]; then \
		$(FETCH) https://packages.typst.org/preview/mitex-$(MITEX_VERSION).tar.gz \
			typst/dl/mitex-$(MITEX_VERSION).tar.gz || exit 1; \
		mkdir -p typst/packages/mitex; \
		tar xzf typst/dl/mitex-$(MITEX_VERSION).tar.gz -C typst/packages/mitex; \
	fi
	@rm -rf typst/dl
	@echo "typst ready: $$(du -sh typst | cut -f1)"

# Vendored browser libraries (DOMPurify, marked, KaTeX, Altcha, zxcvbn) live in
# static/vendor, committed and embedded in the binary. They are pinned in
# assets.sha256 like everything else the browser runs; this re-verifies the
# committed copies in place and re-fetches only one that has gone missing.
# Versions and hashes are recorded in assets.sha256 — bump both together.
VENDOR_NPM := https://cdn.jsdelivr.net/npm
vendor:
	@$(FETCH) $(VENDOR_NPM)/dompurify@3.1.6/dist/purify.min.js  static/vendor/purify.min.js
	@$(FETCH) $(VENDOR_NPM)/marked@12.0.2/marked.min.js         static/vendor/marked.min.js
	@$(FETCH) $(VENDOR_NPM)/katex@0.16.21/dist/katex.min.js     static/vendor/katex.min.js
	@$(FETCH) $(VENDOR_NPM)/katex@0.16.21/dist/katex.min.css    static/vendor/katex.min.css
	@$(FETCH) $(VENDOR_NPM)/altcha@1.0.6/dist/altcha.min.js     static/vendor/altcha.min.js
	@$(FETCH) $(VENDOR_NPM)/zxcvbn@4.4.2/dist/zxcvbn.js         static/vendor/zxcvbn.min.js
	@echo "vendor libraries verified against assets.sha256"

# Everything the browser needs to run code and typeset. Re-running this on a
# deployed instance re-verifies every file against assets.sha256.
assets: vendor pyodide typst

# Local mail catcher: SMTP on 1025, inbox at http://localhost:8025
mail-dev:
	docker start neuroscribe-mail 2>/dev/null || \
	docker run -d --name neuroscribe-mail -p 1025:1025 -p 8025:8025 axllent/mailpit

clean:
	rm -f $(BINARY)
