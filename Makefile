BINARY  := neuroscribe
GO      ?= go
PYTHON  ?= python3

.PHONY: all build release deploy run run-open mail-dev pyodide typst vendor assets test vet fmt check hooks clean \
	app-bundle app-debug app-release app-publish app-version

all: build

build:
	$(GO) build -o $(BINARY) ./cmd/neuroscribe

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
		-o dist/$(BINARY)-$(RELEASE_GOOS)-$(RELEASE_GOARCH) ./cmd/neuroscribe
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
# Plotting. matplotlib is not part of the base download because a note that
# never draws anything should not pay for it — but it is the only thing here
# that can produce a chart, a vector field or a 3D surface, and it composes
# with the numpy, scipy and sympy already alongside it. ~12 MB.
MPL_FILES := \
	cycler-0.12.1-py3-none-any.whl \
	fonttools-4.51.0-py3-none-any.whl \
	kiwisolver-1.4.5-cp312-cp312-pyodide_2024_0_wasm32.whl \
	matplotlib-3.5.2-cp312-cp312-pyodide_2024_0_wasm32.whl \
	matplotlib_pyodide-0.2.2-py3-none-any.whl \
	packaging-23.2-py3-none-any.whl \
	pillow-10.2.0-cp312-cp312-pyodide_2024_0_wasm32.whl \
	pyparsing-3.1.2-py3-none-any.whl

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
# CeTZ draws; cetz-plot puts axes around it. Together they are 126 KB, and they
# are what lets a figure be typeset by the same compiler that typesets the
# document around it — so a plot in a note and the same plot in the PDF are not
# two renderings that agree, they are one rendering.
CETZ_VERSION   := 0.3.4
CETZPLOT_VERSION := 0.1.1
# CeTZ imports this one for string formatting. Fetched because the compiler has
# no package registry and is never allowed to reach for one — see
# scripts/package-manifest.py.
OXIFMT_VERSION := 0.2.1

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
	@if [ ! -f typst/typst_ts_renderer_bg.wasm ]; then \
		$(FETCH) $(TYPST_NPM)/typst-ts-renderer/-/typst-ts-renderer-$(TYPST_VERSION).tgz \
			typst/dl/typst-ts-renderer-$(TYPST_VERSION).tgz || exit 1; \
		tar xzf typst/dl/typst-ts-renderer-$(TYPST_VERSION).tgz -C typst --strip-components=2 \
			package/pkg/typst_ts_renderer_bg.wasm package/pkg/typst_ts_renderer.mjs; \
	fi
	@if [ ! -f typst/packages/cetz/typst.toml ]; then \
		$(FETCH) https://packages.typst.org/preview/cetz-$(CETZ_VERSION).tar.gz \
			typst/dl/cetz-$(CETZ_VERSION).tar.gz || exit 1; \
		mkdir -p typst/packages/cetz; \
		tar xzf typst/dl/cetz-$(CETZ_VERSION).tar.gz -C typst/packages/cetz; \
	fi
	@if [ ! -f typst/packages/cetz-plot/typst.toml ]; then \
		$(FETCH) https://packages.typst.org/preview/cetz-plot-$(CETZPLOT_VERSION).tar.gz \
			typst/dl/cetz-plot-$(CETZPLOT_VERSION).tar.gz || exit 1; \
		mkdir -p typst/packages/cetz-plot; \
		tar xzf typst/dl/cetz-plot-$(CETZPLOT_VERSION).tar.gz -C typst/packages/cetz-plot; \
	fi
	@if [ ! -f typst/packages/oxifmt/typst.toml ]; then \
		$(FETCH) https://packages.typst.org/preview/oxifmt-$(OXIFMT_VERSION).tar.gz \
			typst/dl/oxifmt-$(OXIFMT_VERSION).tar.gz || exit 1; \
		mkdir -p typst/packages/oxifmt; \
		tar xzf typst/dl/oxifmt-$(OXIFMT_VERSION).tar.gz -C typst/packages/oxifmt; \
	fi
	@$(PYTHON) scripts/package-manifest.py \
		typst/packages/cetz typst/packages/cetz-plot typst/packages/oxifmt
	@rm -rf typst/dl
	@echo "typst ready: $$(du -sh typst | cut -f1)"

# Vendored browser libraries (DOMPurify, marked, KaTeX, Altcha, zxcvbn) live in
# static/vendor, committed and embedded in the binary. They are pinned in
# assets.sha256 like everything else the browser runs; this re-verifies the
# committed copies in place and re-fetches only one that has gone missing.
# Versions and hashes are recorded in assets.sha256 — bump both together.
VENDOR_NPM := https://cdn.jsdelivr.net/npm
vendor:
	@$(FETCH) $(VENDOR_NPM)/dompurify@3.1.6/dist/purify.min.js  web/static/vendor/purify.min.js
	@$(FETCH) $(VENDOR_NPM)/marked@12.0.2/marked.min.js         web/static/vendor/marked.min.js
	@$(FETCH) $(VENDOR_NPM)/katex@0.16.21/dist/katex.min.js     web/static/vendor/katex.min.js
	@$(FETCH) $(VENDOR_NPM)/katex@0.16.21/dist/katex.min.css    web/static/vendor/katex.min.css
	@$(FETCH) $(VENDOR_NPM)/altcha@1.0.6/dist/altcha.min.js     web/static/vendor/altcha.min.js
	@$(FETCH) $(VENDOR_NPM)/zxcvbn@4.4.2/dist/zxcvbn.js         web/static/vendor/zxcvbn.min.js
	@echo "vendor libraries verified against assets.sha256"

# Everything the browser needs to run code and typeset. Re-running this on a
# deployed instance re-verifies every file against assets.sha256.
assets: vendor pyodide typst

# Deploying changes to the server: cross-compile here, rsync only what
# differs, restart. The binary embeds every template and script, so a code
# change is one file; the runtime dirs ride along only when their contents
# changed (rsync's delta makes an unchanged 150 MB tree a no-op).
DEPLOY_HOST    ?= vps
DEPLOY_DIR     ?= /srv/neuroscribe
DEPLOY_SERVICE ?= neuroscribe
# One SSH connection for the whole deploy: the first command opens a control
# socket (one passphrase prompt), and every rsync and ssh after it rides the
# same connection. The master lingers two minutes, so a quick redeploy is free.
# The socket lives under ~/.ssh (0700) with a hashed name (%C), not in
# world-writable /tmp where its predictable path invites squatting or hijack.
DEPLOY_SSH := ssh -o ControlMaster=auto -o ControlPath=~/.ssh/ng-deploy-%C -o ControlPersist=120
deploy: release
	rsync -azv -e "$(DEPLOY_SSH)" dist/$(BINARY)-$(RELEASE_GOOS)-$(RELEASE_GOARCH) \
		$(DEPLOY_HOST):$(DEPLOY_DIR)/$(BINARY)
	@for dir in pyodide typst; do \
		if [ -d $$dir ]; then \
			rsync -azv --delete -e "$(DEPLOY_SSH)" $$dir $(DEPLOY_HOST):$(DEPLOY_DIR)/; \
		fi; \
	done
	$(DEPLOY_SSH) $(DEPLOY_HOST) 'mkdir -p $(DEPLOY_DIR)/data \
		&& chown -R root:neuroscribe $(DEPLOY_DIR) \
		&& chown -R neuroscribe:neuroscribe $(DEPLOY_DIR)/data \
		&& chmod 750 $(DEPLOY_DIR) $(DEPLOY_DIR)/$(BINARY) $(DEPLOY_DIR)/data \
		&& systemctl reset-failed $(DEPLOY_SERVICE) 2>/dev/null; \
		systemctl restart $(DEPLOY_SERVICE) \
		&& for i in 1 2 3 4 5 6 7 8 9 10; do \
			$(DEPLOY_DIR)/$(BINARY) healthcheck 2>/dev/null \
				&& echo "healthy after $$i s" && exit 0; \
			sleep 1; \
		done; \
		echo "--- service did not become healthy; recent log: ---"; \
		journalctl -u $(DEPLOY_SERVICE) -n 25 --no-pager; exit 1'
	@echo "deployed and healthy"

# ---- the Android app ----
#
# The app carries its own copy of the frontend, so that installing it is the
# only thing that ever changes it: `make deploy` updates the website and leaves
# every phone exactly as it was. Publishing a new one is deliberate, and the
# version it is published under lives in app.version, which is the only file
# to edit for a release.
#
#   make app-version                 what is in app.version right now
#   make app-bundle                  freeze the frontend into the project
#   make app-debug                   a signed-with-a-throwaway-key APK to try
#   make app-release                 the real one, signed with your key
#   make app-publish                 build it and put it on the server
#
# Signing needs a key you make once and keep, and never commit:
#
#   keytool -genkeypair -v -keystore ~/.neuroscribe/release.jks \
#           -alias neuroscribe -keyalg RSA -keysize 4096 -validity 10000
#
# Android identifies an app by its signature, so if that key is lost, the next
# release cannot be installed over this one by anybody: they have to uninstall
# first, and uninstalling takes the local notes with it. Back it up.

APP_VERSION := $(shell awk -F= '/^VERSION/ {gsub(/[ \t]/,"",$$2); print $$2}' app.version)
APP_ORIGIN  := $(shell awk -F'= *' '/^ORIGIN/ {print $$2}' app.version)
APP_ASSETS  := android/app/src/main/assets/web
APK         := android/app/build/outputs/apk/release/neuroscribe-$(APP_VERSION).apk
# The wrapper if the project has one, otherwise whatever gradle is installed.
# Generate the wrapper once with `cd android && gradle wrapper` if you want the
# build pinned to a version rather than to whatever is on the machine.
GRADLE      ?= $(shell [ -x android/gradlew ] && echo ./gradlew || echo gradle)

app-version:
	@echo "version $(APP_VERSION), syncing with $(APP_ORIGIN)"
	@echo "edit app.version to change it"

# The frozen frontend, written out of the same binary the server runs, so what
# the app ships is exactly what the website would have served at this commit.
app-bundle: build
	@./$(BINARY) bundle $(APP_ASSETS)

app-debug: app-bundle
	@cd android && $(GRADLE) assembleDebug
	@echo "debug APK: android/app/build/outputs/apk/debug/"

app-release: app-bundle
	@if [ -z "$$NEUROSCRIBE_KEYSTORE" ]; then \
		echo "NEUROSCRIBE_KEYSTORE is not set — the APK would be unsigned and"; \
		echo "no phone will install it. See the comments in the Makefile."; \
		exit 1; \
	fi
	@cd android && $(GRADLE) assembleRelease
	@ls -lh $(APK)

# Puts the APK where the download button points. The server serves whatever is
# in downloads/, so this is the step that makes a release public — deliberately
# separate from `make deploy`, which must never change what is on a phone.
app-publish: app-release
	$(DEPLOY_SSH) $(DEPLOY_HOST) 'mkdir -p $(DEPLOY_DIR)/downloads'
	rsync -azv -e "$(DEPLOY_SSH)" $(APK) \
		$(DEPLOY_HOST):$(DEPLOY_DIR)/downloads/neuroscribe.apk
	$(DEPLOY_SSH) $(DEPLOY_HOST) 'printf %s "$(APP_VERSION)" \
		> $(DEPLOY_DIR)/downloads/version.txt \
		&& chown -R root:neuroscribe $(DEPLOY_DIR)/downloads \
		&& chmod 750 $(DEPLOY_DIR)/downloads \
		&& chmod 640 $(DEPLOY_DIR)/downloads/*'
	@echo "published $(APP_VERSION) — the landing page now offers it"

# Local mail catcher: SMTP on 1025, inbox at http://localhost:8025
mail-dev:
	docker start neuroscribe-mail 2>/dev/null || \
	docker run -d --name neuroscribe-mail -p 1025:1025 -p 8025:8025 axllent/mailpit

clean:
	rm -f $(BINARY)

PYODIDE_FILES := pyodide.js pyodide.asm.js pyodide.asm.wasm python_stdlib.zip \
	pyodide-lock.json \
	numpy-1.26.4-cp312-cp312-pyodide_2024_0_wasm32.whl \
	scipy-1.12.0-cp312-cp312-pyodide_2024_0_wasm32.whl \
	pandas-2.2.0-cp312-cp312-pyodide_2024_0_wasm32.whl \
	sympy-1.12-py3-none-any.whl mpmath-1.3.0-py3-none-any.whl \
	openblas-0.3.26.zip six-1.16.0-py2.py3-none-any.whl \
	pytz-2024.1-py2.py3-none-any.whl \
	python_dateutil-2.9.0.post0-py2.py3-none-any.whl \
	$(MPL_FILES)
