package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS users (
	id          INTEGER PRIMARY KEY,
	username    TEXT NOT NULL UNIQUE,
	-- bcrypt of the auth key the browser derives, never of the password
	pass_hash   TEXT NOT NULL,
	kdf_salt    TEXT NOT NULL DEFAULT '',
	-- the account data key, sealed with a key only the password produces
	wrapped_key TEXT NOT NULL DEFAULT '',
	email       TEXT NOT NULL DEFAULT '',
	email_verified INTEGER NOT NULL DEFAULT 0,
	plan        TEXT NOT NULL DEFAULT 'free',
	created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users(lower(email)) WHERE email != '';

-- Everything the reader owns lives here as one opaque record per note,
-- chapter, folder, note type or image. There are no tables for parents,
-- positions or titles: all of that is ciphertext, and a schema that modelled
-- shapes the server cannot read would only describe the account to whoever
-- reads the database. The shape lives inside the sealed payload, reassembled
-- by the browser; what the server keeps is a synchronised bag of sealed blobs.
--
-- What the server needs, and only this:
--   ref        the client-generated address, so a device offline can mint one
--   kind       to count notes and images against a plan
--   seq        a per-account counter: the cursor a device pulls changes from
--   rev        a per-record counter: how a stale write is detected
--   deleted    tombstones, so a deletion reaches other devices
--   payload    sealed bytes it cannot open
CREATE TABLE IF NOT EXISTS records (
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	ref        TEXT NOT NULL,
	kind       TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	rev        INTEGER NOT NULL DEFAULT 1,
	updated_at TEXT NOT NULL,
	deleted    INTEGER NOT NULL DEFAULT 0,
	payload    TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (user_id, ref)
);
CREATE INDEX IF NOT EXISTS idx_records_seq ON records(user_id, seq);
CREATE INDEX IF NOT EXISTS idx_records_kind ON records(user_id, kind, deleted);

-- Image bytes, kept out of the record stream so a first sync does not have to
-- carry megabytes of base64 before the first note can be read. A blob is
-- fetched only when a note that references it is opened.
CREATE TABLE IF NOT EXISTS blobs (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	ref     TEXT NOT NULL,
	data    BLOB NOT NULL,
	PRIMARY KEY (user_id, ref)
);

CREATE TABLE IF NOT EXISTS sessions (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS verifications (
	token_hash TEXT PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	purpose    TEXT NOT NULL DEFAULT 'verify',
	expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verifications_user ON verifications(user_id);

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS user_settings (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	key     TEXT NOT NULL,
	value   TEXT NOT NULL,
	PRIMARY KEY (user_id, key)
);
`

// store is the data layer bound to one account. Every record is reachable
// only through it, so a query cannot forget whose data it is touching: the
// owner is part of every statement.
type store struct {
	db  *sql.DB
	uid int64
}

// newRef mints an address that says nothing about what it points to.
func newRef() string {
	buf := make([]byte, 9)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

func (st *store) setting(key, def string) string {
	var v string
	if err := st.db.QueryRow("SELECT value FROM user_settings WHERE user_id = ? AND key = ?",
		st.uid, key).Scan(&v); err != nil {
		return def
	}
	return v
}

func (st *store) setSetting(key, value string) {
	st.db.Exec(`INSERT INTO user_settings (user_id, key, value) VALUES (?, ?, ?)
		ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value`, st.uid, key, value)
}

func getSetting(db *sql.DB, key, def string) string {
	var v string
	if err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v); err != nil {
		return def
	}
	return v
}

func setSetting(db *sql.DB, key, value string) {
	db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
}

func openDB(path string) *sql.DB {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	// modernc.org/sqlite is happiest with a single writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		log.Fatalf("init schema: %v", err)
	}
	assertSchemaCurrent(db)
	purgeTrash(db)
	purgeUnverified(db)
	return db
}

// assertSchemaCurrent fails loudly when the file predates this build. There
// are no migrations by design, so the honest options are a fresh database or
// an older binary — silently running with half a schema is not one of them.
func assertSchemaCurrent(db *sql.DB) {
	probes := map[string]string{
		"users":   "SELECT plan, kdf_salt, wrapped_key FROM users LIMIT 1",
		"records": "SELECT user_id, ref, kind, seq, rev, deleted, payload FROM records LIMIT 1",
		"blobs":   "SELECT user_id, ref, data FROM blobs LIMIT 1",
	}
	for table, probe := range probes {
		if _, err := db.Exec(probe); err != nil {
			log.Fatalf("database schema is older than this build (%s: %v).\n"+
				"There are no migrations: start a fresh NEUROSCRIBE_DB, or run a build that matches the file.", table, err)
		}
	}
}

// altchaKey returns the persistent HMAC key for captcha challenges, minting
// one on first use so restarts do not invalidate outstanding challenges.
func altchaKey(db *sql.DB) []byte {
	key := getSetting(db, "altcha_key", "")
	if key == "" {
		key = newSessionToken()
		setSetting(db, "altcha_key", key)
	}
	return []byte(key)
}

const trashRetentionDays = 60

// purgeTrash drops tombstones that every device has had ample time to see.
// The record is already deleted as far as readers are concerned; this only
// stops the change log growing forever. Blobs go with them.
func purgeTrash(db *sql.DB) {
	cutoff := fmt.Sprintf("-%d days", trashRetentionDays)
	db.Exec(`DELETE FROM blobs WHERE ref IN (
		SELECT ref FROM records WHERE deleted = 1 AND updated_at < datetime('now', ?))`, cutoff)
	db.Exec("DELETE FROM records WHERE deleted = 1 AND updated_at < datetime('now', ?)", cutoff)
}
