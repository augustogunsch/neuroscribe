# Deploying Neuroscribe

A short path from a fresh VPS to a running instance. The longer explanations
live in [README.md](README.md); this is the order to do things in.

Neuroscribe is one Go binary, one SQLite file, and a directory of static
runtime files. No daemon it talks to, no interpreter it shells out to, no
queue. TLS is **not optional**: the browser derives encryption keys with
WebCrypto and installs the offline app with a service worker, and browsers
allow both only in a secure context. Plain HTTP on a public host gives you
neither sign-in nor offline mode.

## 1. Build — on your machine, not the server

The binary is fully static (no cgo, no libc), so build where the cores are
and copy the result:

```sh
git clone <your-remote> neuroscribe && cd neuroscribe
make release          # → dist/neuroscribe-linux-amd64 (needs Go 1.26.6+)
make assets           # ~150 MB: the browser's Python runtime + typesetter,
                      # every file verified against assets.sha256
scp dist/neuroscribe-linux-amd64 you@server:neuroscribe
```

Do not compile on a small VPS. The pure-Go SQLite this app uses is one of the
heaviest packages in the ecosystem to build: on one slow vCPU it takes minutes,
and on a box with ≤1 GB of RAM the compiler is OOM-killed outright — the
symptom is `modernc.org/libc: …/compile: signal: killed`. If you must build
there anyway, give it swap first:

```sh
fallocate -l 2G /swapfile && chmod 600 /swapfile
mkswap /swapfile && swapon /swapfile
```

`make assets` is optional. Without it the app runs; Python snippets and PDF
export report themselves unavailable. `make release` defaults to linux/amd64;
override with `RELEASE_GOARCH=arm64` for an ARM server.

## 2. Install

The service account must **not** be able to overwrite its own binary or read
config it does not need — that is the best persistence foothold a compromised
process could ask for. So the binary and `.env` are owned by `root` and only
*read* by the service; the one thing the service writes, its database, lives in
a `data/` subdirectory it owns.

```sh
sudo useradd --system --home /srv/neuroscribe --shell /usr/sbin/nologin neuroscribe
sudo mkdir -p /srv/neuroscribe/data

# binary + static runtimes: owned by root, readable/traversable by the service
sudo cp neuroscribe /srv/neuroscribe/
sudo cp -r pyodide typst /srv/neuroscribe/   # if you ran `make assets`
sudo chown -R root:neuroscribe /srv/neuroscribe
sudo chmod 750 /srv/neuroscribe /srv/neuroscribe/neuroscribe   # dirs need x to enter
sudo find /srv/neuroscribe/pyodide /srv/neuroscribe/typst -type d -exec chmod 755 {} + 2>/dev/null || true

# the only writable place: the database directory, owned by the service
sudo chown -R neuroscribe:neuroscribe /srv/neuroscribe/data
sudo chmod 750 /srv/neuroscribe/data

# .env holds the SMTP secret: root-owned, group-readable by the service only
sudo install -o root -g neuroscribe -m 640 /dev/null /srv/neuroscribe/.env
sudo -e /srv/neuroscribe/.env    # or your editor of choice; see the template below
```

Fill `/srv/neuroscribe/.env` (owner `root:neuroscribe`, mode `0640`):

```sh
NEUROSCRIBE_ADDR=127.0.0.1:8484
NEUROSCRIBE_DB=/srv/neuroscribe/data/neuroscribe.db
NEUROSCRIBE_ALLOWED_HOSTS=notes.example.com
NEUROSCRIBE_BASE_URL=https://notes.example.com
NEUROSCRIBE_TRUST_PROXY=1

# sign-up needs working mail (Proton: port 465, not 587)
NEUROSCRIBE_REGISTRATION=open
NEUROSCRIBE_SMTP_HOST=smtp.protonmail.ch
NEUROSCRIBE_SMTP_PORT=465
NEUROSCRIBE_SMTP_USER=you@yourdomain.com
NEUROSCRIBE_SMTP_PASS=<smtp token from Proton settings>
NEUROSCRIBE_MAIL_FROM=Neuroscribe <you@yourdomain.com>
```

## 3. Run it as a service

`/etc/systemd/system/neuroscribe.service`:

```ini
[Unit]
Description=Neuroscribe
After=network-online.target
Wants=network-online.target

[Service]
User=neuroscribe
WorkingDirectory=/srv/neuroscribe
EnvironmentFile=/srv/neuroscribe/.env
ExecStart=/srv/neuroscribe/neuroscribe
Restart=on-failure

# the process writes only its database directory; everything else is read-only
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
ReadWritePaths=/srv/neuroscribe/data
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
# a pure-Go static binary tolerates the full lockdown, so take all of it
SystemCallFilter=@system-service
SystemCallArchitectures=native
RestrictNamespaces=yes
LockPersonality=yes
ProtectHostname=yes
ProtectClock=yes
ProtectProc=invisible
ProcSubset=pid
RestrictRealtime=yes
RestrictSUIDSGID=yes
UMask=0077

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now neuroscribe
curl -s http://127.0.0.1:8484/healthz    # → ok
```

If it fails, `journalctl -u neuroscribe -n 30` says why. Two systemd codes
worth decoding: `status=200/CHDIR` means `/srv/neuroscribe` is missing or
lacks the execute bit (directories need `x` to be entered — `chmod 750` it);
`Failed to load environment files` means `.env` does not exist. After five
fast failures systemd latches `start-limit-hit` and refuses further starts:
`systemctl reset-failed neuroscribe` clears it before the next restart.

(Prefer containers? `compose.yaml` in the repo is the hardened equivalent:
`docker compose up -d --build`.)

## 4. nginx and TLS

**Already running nginx with other certificates?** Skip the bootstrap: the
nginx authenticator answers the challenge through your live server without
touching your other sites —

```sh
sudo certbot certonly --nginx -d <your domain>
```

— then jump to installing the site config below. (If your other sites renew
through a shared webroot instead, reuse it: pass your `-w` path to certbot
and change the `root` under `/.well-known/acme-challenge/` in the site config
to match, so renewals keep working.)

**On a fresh box** the site config references the certificate, and certbot
needs nginx serving its challenge to issue one — so the first certificate
goes through a bootstrap config that serves only the challenge:

```sh
sudo mkdir -p /var/www/certbot
sudo cp deploy/nginx-bootstrap.conf /etc/nginx/sites-enabled/certbot-bootstrap.conf
sudo sed -i 's/notes.example.com/<your domain>/' /etc/nginx/sites-enabled/certbot-bootstrap.conf
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/certbot -d <your domain>
sudo rm /etc/nginx/sites-enabled/certbot-bootstrap.conf
```

With the certificate on disk, the real config loads:

```sh
sudo cp deploy/nginx.conf /etc/nginx/sites-available/neuroscribe
sudo sed -i 's/notes.example.com/<your domain>/g' /etc/nginx/sites-available/neuroscribe
sudo ln -s /etc/nginx/sites-available/neuroscribe /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

If certbot fails with `cannot load certificate
"/etc/letsencrypt/live/<domain>/fullchain.pem" … no such file` from
`nginx -t`, the site config got enabled before its certificate existed, and
no issuance method can run on a broken nginx. Take it out of the way, issue,
put it back:

```sh
sudo rm /etc/nginx/sites-enabled/neuroscribe
sudo systemctl reload nginx
sudo certbot certonly --nginx -d <your domain>
sudo ln -s /etc/nginx/sites-available/neuroscribe /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

Renewals need no bootstrap: the real config keeps the challenge path served
over plain HTTP, and `certbot renew` reuses whichever method issued the
certificate. The supplied config already carries the pieces that matter: `Host` and
`X-Forwarded-Proto` forwarded (host allowlisting and Secure cookies depend on
them), rate limits on sign-in and sign-up, `sw.js` never cached, sync bodies
kept off nginx's disk.

If the site answers **"unrecognized Host header"**, everything is connected
and the app is rejecting the domain: `NEUROSCRIBE_ALLOWED_HOSTS` in `.env`
must be exactly the bare hostname (`notes.example.com` — no scheme, no port),
and the app only rereads `.env` on restart. It coexists with other sites on the same nginx: it
claims only its own `server_name`, and its rate-limit zones carry the `ng_`
prefix so they cannot collide with zones your other configs define.

## 5. First account, and checks

Open `https://<your domain>`, create the account, click the link in the
verification mail. Then confirm the deployment is what it claims to be:

```sh
# the runtime files still match their pins (also re-verifies vendored JS)
cd /srv/neuroscribe && make assets vendor

# the database holds no plaintext: write a note, then
sqlite3 data/neuroscribe.db "SELECT substr(payload,1,40) FROM records LIMIT 3;"
# every row should read {"h":"v1.… or {"h":"v2.… — sealed envelopes, nothing legible
```

In the browser: the address bar offers *Install*; after loading once, the app
opens with the network disabled. That is the deployment working, not a cache
accident.

## Upkeep

- **Backups**: copy `data/neuroscribe.db` (plus `-wal`/`-shm`, or use
  `sqlite3 data/neuroscribe.db ".backup backup.db"` for a consistent snapshot).
  The file is ciphertext except account emails; treat it as sensitive anyway.
- **Upgrades**: build the new binary, restart the service. Asset caches
  invalidate themselves (the version is a hash of the build); browsers pick
  the new app up on next load. There are no database migrations by design —
  the schema changing is a breaking release, and the README says so.
- **Accounts**: there is no admin interface. `sqlite3` one-liners in the
  README cover verify/plan/delete. Passwords cannot be reset — the server
  never had them, which is the point of the whole design.
