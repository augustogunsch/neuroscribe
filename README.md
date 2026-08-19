# Neuroscribe

The definitive knowledge database for any subject. A single Go binary that
serves a Markdown-based knowledge base: notes organized in folders, split into
chapters, with LaTeX math, highlighted code, Python and JavaScript snippets
that run in the browser, image attachments, and typeset PDF export — which
also runs in the browser, so a note is never sent anywhere to be printed.

Deploying to a server? [DEPLOY.md](DEPLOY.md) is the short path: build,
systemd unit, nginx, TLS, first account, verification.

## Run

```sh
make build            # or: go build -o neuroscribe .
./neuroscribe
# → http://127.0.0.1:8484
```

Open it in a browser and install it (the address bar offers this, or
*Add to Home Screen* on a phone). Installed or not, the app keeps working
without a connection once it has been opened successfully once — installing
only gives it a window of its own.

Note that a service worker needs a secure context: it runs on `localhost`, and
over HTTPS anywhere else. Served over plain HTTP on a public host there is no
offline mode, and no encryption either (see the TLS note in `deploy/`).

Accounts are created only through the sign-up form (see below). There is no
account-management CLI: a password cannot be rotated server-side at all, since
only the browser holds the key that unwraps the data key, and everything else
is one SQL statement against the database file:

```sh
sqlite3 neuroscribe.db "SELECT username, plan, created_at FROM users"
sqlite3 neuroscribe.db "UPDATE users SET email_verified = 1 WHERE username = 'you'"
sqlite3 neuroscribe.db "UPDATE users SET plan = 'premium' WHERE username = 'you'"
sqlite3 neuroscribe.db "DELETE FROM users WHERE username = 'someone'"
```

Deleting a user takes their notes, chapters and images with them (foreign keys
cascade). To force a signed-in session out, delete its row from `sessions`.

### Public registration

Sign-ups at `/register` need three things: a configured mailer, the opt-in
switch, and a base URL for the links in the messages.

```sh
export NEUROSCRIBE_SMTP_HOST=smtp.resend.com   # Resend: 3k mails/month free
export NEUROSCRIBE_SMTP_PORT=587               # 465 uses implicit TLS
export NEUROSCRIBE_SMTP_USER=resend            # Resend uses this literal user
export NEUROSCRIBE_SMTP_PASS=re_xxxxxxxx       # the API key
export NEUROSCRIBE_MAIL_FROM='Neuroscribe <noreply@your-domain>'
export NEUROSCRIBE_BASE_URL=https://notes.your-domain
export NEUROSCRIBE_REGISTRATION=open
```

Any SMTP provider works — only these variables change. With a **Proton Mail
Business** plan, generate an SMTP submission token (Settings → Mail →
IMAP/SMTP) and use it directly:

```sh
export NEUROSCRIBE_SMTP_HOST=smtp.protonmail.ch
export NEUROSCRIBE_SMTP_PORT=465               # implicit TLS; see the note below
export NEUROSCRIBE_SMTP_USER=you@your-domain   # the address the token belongs to
export NEUROSCRIBE_SMTP_PASS=<smtp token>      # not your account password
export NEUROSCRIBE_MAIL_FROM='Neuroscribe <you@your-domain>'
```

Port 465 is the default because 587 is widely interfered with: some networks
accept the TCP connection and then silently swallow the SMTP conversation, so
submission hangs rather than failing. Connections are bounded by timeouts and
every attempt is logged, so a stall like that shows up as an error instead of
a request that never returns.

Proton requires the `From` address to match the token's address. Individual
Proton plans do not offer SMTP submission — those need Proton Bridge, which is
not suitable for a server.

Verify credentials before opening sign-ups:

```sh
./neuroscribe mail test you@example.com
```

Settings live in `.env.local` (untracked, sourced by `make run`); copy
`.env.example` to start. STARTTLS is negotiated automatically, and the mailer
refuses to send credentials in the clear to anything but a loopback relay.

New accounts are inactive until the emailed link is clicked, unconfirmed ones
are deleted after 7 days, and each sign-up must solve an
[Altcha](https://altcha.org) proof-of-work challenge (vendored locally, no
third-party requests). Registration is rate limited to 5 attempts per hour per
IP address.

> **Multi-account:** every record and image is scoped to its owner (`user_id`
> on `records`/`blobs`, and every query runs through an account-bound store), so
> accounts are isolated — one cannot read, sync, or overwrite another's data.
> Opening `NEUROSCRIBE_REGISTRATION=open` is therefore safe on this axis; it is
> still gated by email verification, an Altcha proof-of-work, and rate limiting.

> **There is no CLI fallback for creating accounts.** If mail breaks while no
> account exists, fix mail (`neuroscribe mail test …`) — nothing else can mint
> the first login.

Configuration (environment variables):

| Variable | Default | Purpose |
|---|---|---|
| `NEUROSCRIBE_ADDR` | `127.0.0.1:8484` | Listen address |
| `NEUROSCRIBE_DB` | `neuroscribe.db` | SQLite database path |
| `NEUROSCRIBE_PYODIDE_DIR` | `pyodide` | Where the browser's Python runtime is served from |
| `NEUROSCRIBE_ALLOWED_HOSTS` | `localhost,127.0.0.1` | Host header allowlist (anti DNS-rebinding) |
| `NEUROSCRIBE_BASE_URL` | `http://<addr>` | Public URL used in emailed links |
| `NEUROSCRIBE_REGISTRATION` | unset | `open` enables public sign-up |
| `NEUROSCRIBE_SMTP_*`, `NEUROSCRIBE_MAIL_FROM` | unset | Outgoing mail (see below) |

The database is created from a single schema — there is no migration path
from earlier versions, so start from a fresh `neuroscribe.db`.

Optional host dependencies:

- **The Python runtime** — `make pyodide` downloads CPython compiled to
  WebAssembly plus **numpy, scipy, sympy and pandas** into `./pyodide`
  (~116 MB, not in git). The server only serves those files; the interpreter
  runs in the reader's browser. Without them JavaScript snippets still run and
  Python reports itself unavailable.
- `make typst` fetches the **Typst** typesetter (WebAssembly), the New
  Computer Modern fonts and the **mitex** package into `./typst` (~33 MB, not
  in git). The browser typesets, so the server needs no TeX installation and
  serves nothing but static files. Without it PDF export reports itself
  unavailable.

`make assets` fetches both. Both are detected at startup; the UI degrades
gracefully without them, and every file is checked against `assets.sha256`.

## Features

- **Folders & notes** — arbitrary directory tree in the sidebar (resizable,
  collapsible); drag a note onto a folder (or the empty tree area for the
  root) to move it. Notes live at `/notes/<folder-path>/<slug>` and chapters
  at `/notes/<folder-path>/<note>/<chapter>`; note titles must be unique per
  folder and chapter titles unique per note (slug-level, accent-insensitive),
  and moves that would collide are rejected.
- **Trash** — deleting a note (or a folder with notes) moves the notes to
  the trash; restore them from `/trash` within 60 days, after which they are
  purged permanently.
- **Note types & metadata** — every note has a type (defaults: *Note* and
  *Book record* with author, edition, year, and finished-reading fields) plus
  a title and description. Types are user-defined at
  `/types`: each type declares extra fields (text, number, date, url,
  checkbox) whose schema is stored as JSON; values are validated server-side
  and shown on the note's metadata card and in the PDF title block.
- **Chapters** — each note is an ordered list of chapters. The note page is
  a table of contents; every chapter is its own page with prev/next
  navigation and in-place editing, reorderable from the note page.
- **Markdown** — GFM (tables, task lists, strikethrough, autolinks), parsed in
  the browser by marked, because the server cannot read a note to render it.
- **Math** — `$…$` inline and `$$…$$` display (multi-line environments
  supported), typeset client-side by KaTeX from escaped text. Display
  equations with `\label{name}` are auto-numbered and `\eqref{name}`
  renders as a link to the equation. On the web
  references resolve within a chapter (numbered 1, 2, …); in the exported
  PDF they resolve across the whole note, printing as "Equation 1" and
  linking to it.
- **Code** — fenced blocks highlighted with chroma for any language.
- **Snippets** — `python` and `javascript` blocks get a *Run* button, and both
  languages execute in the browser. Python is CPython compiled to WebAssembly
  with numpy, scipy, sympy and pandas available; JavaScript is the browser's
  own engine. Neither the code nor its output is ever sent to the server, and
  neither can reach the note: execution happens inside a worker, inside a
  frame sandboxed to an opaque origin, with a 60 s (Python) / 10 s
  (JavaScript) limit that terminates a runaway loop.
- **PDF export** — the whole note (all chapters as numbered headings, with a
  title page, metadata block and table of contents), converted Markdown→Typst
  and compiled in the browser; attached images are embedded and code keeps its
  highlighting. Formulas stay written in LaTeX — mitex translates them — and
  the result is set in Computer Modern — the classic LaTeX look. Nothing is
  sent anywhere.
- **Plans** — quotas keep one account from filling the disk, and appear only in
  settings. Free: 500 notes, 20 images of 5 MiB, 500 KiB per chapter. Premium:
  5000 notes, 500 images of 10 MiB, 8 MiB per chapter. Set the `plan` column
  in the `users` table.
- **Password policy** — sign-up measures the password with zxcvbn rather than
  demanding a capital and a symbol, and refuses anything below its top score.
  Composition rules produce `P@ssw0rd1`; four unrelated words score full marks
  and are easier to remember. The check has to be in the browser because the
  server never receives the password — and there is no reset, so it is the only
  check there will ever be.
- **Device PIN** — six digits unlock a browser instead of the full password,
  optionally after an idle timeout. Locking is not a screen cover: the data key
  is wiped from `sessionStorage` and from memory, unsaved editor text is sealed
  with it first, and the page is reloaded so the decrypted DOM is replaced by
  the ciphertext the server sends. What stays on the device is the data key
  sealed under the PIN, in `localStorage`; the PIN itself is never stored and
  never sent. See *What the PIN is worth* below.
- **Images** — upload from the editor toolbar (inserted as Markdown at the
  cursor) or the note page's Images grid; stored as blobs in SQLite, served
  at `/images/{id}/{name}`. PNG/JPEG/GIF/WebP, 5 MiB and 20 images per
  account, content-sniffed.
- **Markdown editor** — toolbar (bold, italic, strikethrough, heading, list,
  quote, code, code block, math, link, image upload) plus ⌘/Ctrl+B, +I, +K
  shortcuts.
- **Keyboard navigation** — ←/→ move between chapters, `u` goes up to the
  note.
- **Settings** (`/settings`) — language (English, Português), light/dark/auto
  color scheme, note type management, and **full export**: a zip of the whole
  database with the folder tree mirrored, each note as `.md` (YAML front
  matter + chapters) and typeset `.pdf`, plus all images under `_images/`.
- **Login** — session-based auth (bcrypt, hashed session tokens, HttpOnly
  SameSite=Lax cookies); accounts are created from the CLI only.

## Development

`make check` runs the quality gate (gofmt, go vet, go test); `make hooks`
installs it as a git pre-commit hook (`.githooks/pre-commit`).

## Architecture

The browser is the application: vanilla JavaScript builds every page from the
local store, and the server is a small Go binary that authenticates, serves
static assets and syncs sealed records. No framework on either side. Storage
is SQLite on the server (`modernc.org/sqlite`, pure Go — no cgo) and IndexedDB
in the browser. Templates and static assets are embedded in the binary.

```
main.go       entrypoint
handlers.go   routing, security headers, CSRF
shell.go      the one page every app address returns, plus /account
sync.go       the sync API: pull, push, blobs — all the server does with notes
db.go         SQLite: accounts, sessions, and one opaque record store
auth.go       sign-in, sessions
register.go   public sign-up, email confirmation
altcha.go     proof-of-work captcha challenges
mail.go       SMTP delivery
runner.go     serves the browser's Python runtime and typesetter
templates/    the shell, and the pages that exist before an account does

static/
  sw.js        service worker: caches the shell, answers every navigation
  store.js     IndexedDB — the source of truth
  sync.js      pushes what is dirty, pulls what is new, resolves conflicts
  model.js     the decrypted shape of the account, in memory only
  views.js     the pages (was templates/)
  router.js    addresses, dialogs, images, global actions
  settings.js  settings, note types, offline runtime switches
  render.js    Markdown → sanitised DOM → KaTeX
  crypto.js    key derivation, sealing, opening
  lock.js      the PIN lock
  export.js    zip and PDF, both built from this device's copy
```

## How offline works

The server answers **every** address inside the app with the same document.
That document contains no notes — it is a shell. The service worker caches it
once and serves it for every navigation, which is why `/notes/<ref>` works for
a note this device has never asked the server about, including one written on
a train.

What fills the shell comes from IndexedDB. Records are stored exactly as they
travel: a plain envelope (address, kind, revision, dates) around a sealed
payload. The envelope deliberately says nothing about which folder a note is
in or what it is called — a tree the server could read would describe the
account almost as well as the notes would — so the shape lives inside the
sealed part and is reassembled in memory after unlocking.

Writing never waits for a network. It lands in IndexedDB and returns; sync
runs afterwards and may fail freely.

**Synchronisation** is two calls. `GET /sync?since=N` returns every record
changed after cursor N, oldest first, so a pull is resumable and costs nothing
when there is nothing new. `POST /sync` sends a batch, each record carrying the
revision it was based on. A record that moved underneath is refused and handed
back rather than overwritten — the remote version keeps the address and the
local edit is kept beside it as a conflict copy. Nothing is discarded to make
a merge easier.

Deletions travel as tombstones, or a device that was offline would resurrect
what another device removed.

**Images** are kept out of that stream: a first sync should be a list of notes,
not a download of every picture ever attached to one. The bytes come down the
first time a note that uses them is opened, from `/sync/blob/<ref>`.

**The heavy runtimes** — Python (116 MB) and the typesetter (33 MB) — are never
cached without being asked for. Settings has a switch for each; the service
worker downloads and keeps them, and survives an app upgrade without
re-fetching them.


## License

GNU General Public License v3.0 — see [LICENSE](LICENSE).

## Security

- **Markdown/XSS**: notes are rendered in the browser, so DOMPurify is the
  whole boundary — and the page it protects holds the key to every note. It
  runs as an *allowlist*: a fixed set
  of tags and attributes (no `svg`, `math`, `iframe`, `style` or form controls),
  a URI pattern that admits only http(s), mailto and relative links, `class`
  narrowed to the `language-*` marker a code fence needs, and `data-*` refused
  outright — `app.js` dispatches on `[data-action]`, so a note carrying one
  would be driving the app. The result is inserted as a DOM fragment, never as
  a string, so the browser never re-parses what was just sanitized; external
  links get `rel="noopener noreferrer nofollow"`. If DOMPurify fails to load,
  rendering refuses rather than falling back.
- **CSP**: the page holding the keys gets a plain `script-src 'self'` — no
  `eval`, no `wasm-unsafe-eval`. Both things that need to build code from a
  string are pushed off it: the typesetter into a module worker and the snippet
  runner into a sandboxed frame, each with its own narrower policy on its own
  response. KaTeX, marked, DOMPurify, Altcha and zxcvbn are vendored under
  `static/vendor/`, so no third-party origin can execute code here and there is
  nothing to pin with SRI. No inline scripts or event handlers anywhere.
  (`style-src` keeps `'unsafe-inline'`: KaTeX styles the spans it renders.)
- **DNS rebinding**: requests whose `Host` header is not in the allowlist are
  refused, so a hostile page cannot point its own domain at this server and
  drive it from the victim's browser.
- **CSRF**: every unsafe method requires a double-submit token (cookie +
  `X-CSRF-Token` header echoed by same-origin script) *and* passes an
  Origin/Referer check. `form-action 'self'` and `base-uri 'none'` round it out.
- **Login throttle**: 5 failed attempts per (IP, username) trigger a 15-minute
  lockout, on top of the dummy-bcrypt timing equalization.
- **Registration**: Altcha proof of work (HMAC-signed challenges, single-use
  solutions, 15-minute expiry), address confirmation before the account works,
  per-IP rate limiting, and identical responses whether or not an address is
  already registered, so sign-up cannot be used to enumerate users.
- **Snippets**: the server cannot run them — there is no execution endpoint.
  In the browser they run inside a worker inside an iframe sandboxed with
  `allow-scripts` alone, which gives it an opaque origin: no cookies, no
  storage, no DOM, and requests back to this app are cross-origin and fail.
  That frame is the only document served with `unsafe-eval`, which is what an
  interpreter needs and what the isolation pays for; `frame-ancestors 'self'`
  keeps anyone else from embedding it.
- **Typesetting**: Typst is given note text as string literals and escaped
  markup, and compiles with no network and no file system beyond the fonts and
  the vendored mitex package mapped into it. Formulas stay in LaTeX and are
  translated by mitex, which is a parser rather than a macro engine — there is
  no `\input`, no `\write18`, and no shell to escape to.
- **Auth**: bcrypt password hashes, 256-bit session tokens stored hashed,
  HttpOnly SameSite=Lax cookies that gain the `Secure` flag automatically over
  TLS (direct or via `X-Forwarded-Proto`). Serve over HTTPS and set
  `NEUROSCRIBE_ALLOWED_HOSTS` if exposed beyond localhost.
- **Exports and plaintext**: there is none. No route accepts note text —
  Markdown rendering, snippet execution, PDF typesetting and zip export all
  happen in the browser, so the server never holds a decrypted note, even
  briefly. There is nothing to wipe, shred or zero, because nothing arrives.
- **Supply chain**: `make assets` downloads a Python interpreter and a
  typesetter that then execute in every reader's browser, from a CDN, an npm
  registry and a mutable git branch. Every file is pinned by SHA-256 in
  `assets.sha256`; `scripts/fetch.sh` installs nothing unlisted, verifies
  archives before they reach `tar`, and re-checks what is already on disk, so
  re-running the target audits a deployment. A test fails if a fetch is added
  without a pin.
- **What synchronising reveals**: the server orders changes with a counter and
  timestamps them, so it can see how many records an account has, roughly how
  large each is, which kind each is (a note, a chapter, an image), and when
  each changed. That is the price of a resumable sync, and it is the same
  shape the disclosure table in settings describes: how much you write and
  when, never what.
- **What the PIN is worth**: six digits is a million combinations. Against
  someone who picks up your unlocked laptop that is plenty, and ten wrong tries
  erase the sealed key from the device. Against someone who copies this
  browser's storage and guesses offline it is not much: the KDF is set to 3
  million PBKDF2 rounds, which turns the full space from minutes into hours,
  and hours is not safety. The PIN is a convenience lock on top of the
  password, which remains the only thing that actually protects the data. It is
  cleared on sign-out.
- **Not covered**: the SQLite file is unencrypted at rest — use full-disk
  encryption if that matters.
- **Uploads**: content-type sniffed against an image allowlist, 10 MiB cap,
  served with nosniff and a fixed Content-Type.
