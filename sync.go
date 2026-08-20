package main

// Synchronisation.
//
// The browser is the source of truth now. It holds every note in IndexedDB,
// renders from there, and works with the network switched off; this file is
// the other half — a place to leave sealed records so a second device, or the
// same device after a reinstall, can catch up.
//
// The protocol is deliberately small:
//
//	GET  /sync?since=N   every record changed after cursor N, oldest first
//	POST /sync           a batch of local changes, each with the rev it was
//	                     based on; the server rejects any that moved underneath
//	GET  /sync/blob/{ref}   image bytes, fetched only when a note needs them
//	PUT  /sync/blob/{ref}   image bytes, uploaded once
//
// Ordering is by `seq`, a counter per account. A device remembers the highest
// seq it has seen and asks for everything after it, so a pull is resumable and
// costs nothing when there is nothing new. `rev` is per record and is how a
// stale write is caught: a device that edits a chapter it last saw at rev 3
// sends base_rev 3, and if the server has since moved to rev 4 the write is
// refused and returned, rather than quietly overwriting whatever the other
// device wrote.
//
// The server can read none of it. It sees kinds and sizes because plans are
// counted in notes and images, and it sees when records change because a
// cursor has to order them somehow.

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// kinds the client may store. An unknown kind is refused rather than stored,
// so a bug on one device cannot fill an account with junk the others ignore.
var recordKinds = map[string]bool{
	"dir": true, "note": true, "chapter": true, "type": true, "image": true,
	// "pin" carries one device's lock settings. Like every other payload it
	// is sealed with the data key, so what the server holds is the fact that
	// a lock exists and nothing about it — not the digits, and above all not
	// the data key wrapped under them, which never leaves the device that
	// set it. See the note in static/pin.js.
	"pin": true,
}

const (
	syncPageSize  = 500
	maxSyncBody   = 24 << 20
	maxPayloadLen = 512 << 10 // one sealed record; images keep bytes in blobs
	maxBlobBody   = 12 << 20  // the largest plan's image, plus room for the seal
	maxPinRecords = 20        // devices that may hold a PIN for one account
)

// Addresses are minted by the client, so their shape is enforced here: the
// base64url alphabet and nothing else. Anything looser would let a hostile
// client salt the database — and other devices' URLs — with junk.
var refShape = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Timestamps also arrive from the client (a note written offline is older
// than its sync). They are compared lexically all over, so a malformed one is
// replaced with the server's clock rather than stored.
var timeShape = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)

type syncRecord struct {
	Ref       string `json:"ref"`
	Kind      string `json:"kind"`
	Rev       int64  `json:"rev"`
	Seq       int64  `json:"seq"`
	UpdatedAt string `json:"updated_at"`
	Deleted   bool   `json:"deleted"`
	Payload   string `json:"payload"`
	// push only: the rev this edit was based on. 0 means "this is new".
	BaseRev int64 `json:"base_rev,omitempty"`
}

type pullResponse struct {
	Cursor  int64        `json:"cursor"`
	Records []syncRecord `json:"records"`
	More    bool         `json:"more"`
}

type pushRequest struct {
	Records []syncRecord `json:"records"`
}

type pushResponse struct {
	Cursor    int64        `json:"cursor"`
	Applied   []syncRecord `json:"applied"`
	Conflicts []syncRecord `json:"conflicts"`
	Rejected  []rejection  `json:"rejected"`
}

type rejection struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason"`
}

/* ---- pull ---- */

func (s *server) syncPull(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	st := s.st(r)

	rows, err := st.db.Query(`SELECT ref, kind, rev, seq, updated_at, deleted, payload
		FROM records WHERE user_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		st.uid, since, syncPageSize+1)
	if err != nil {
		httpError(w, 500, "could not read changes")
		return
	}
	defer rows.Close()

	out := pullResponse{Cursor: since, Records: []syncRecord{}}
	for rows.Next() {
		var rec syncRecord
		var deleted int
		if err := rows.Scan(&rec.Ref, &rec.Kind, &rec.Rev, &rec.Seq,
			&rec.UpdatedAt, &deleted, &rec.Payload); err != nil {
			httpError(w, 500, "could not read changes")
			return
		}
		rec.Deleted = deleted == 1
		if len(out.Records) == syncPageSize {
			// one past the page: there is more, and the cursor stops here
			out.More = true
			break
		}
		out.Records = append(out.Records, rec)
		out.Cursor = rec.Seq
	}
	writeJSON(w, out)
}

/* ---- push ---- */

func (s *server) syncPush(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSyncBody))
	if err != nil {
		httpError(w, 400, "could not read the request")
		return
	}
	var req pushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpError(w, 400, "malformed sync batch")
		return
	}
	st := s.st(r)
	plan := st.plan()
	resp := pushResponse{Applied: []syncRecord{}, Conflicts: []syncRecord{}, Rejected: []rejection{}}

	tx, err := st.db.Begin()
	if err != nil {
		httpError(w, 500, "could not start a transaction")
		return
	}
	defer tx.Rollback()

	seq := nextSeq(tx, st.uid)
	counts := liveCounts(tx, st.uid)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	for _, in := range req.Records {
		if !recordKinds[in.Kind] || !refShape.MatchString(in.Ref) {
			resp.Rejected = append(resp.Rejected, rejection{in.Ref, "unknown kind or address"})
			continue
		}
		if len(in.Payload) > maxPayloadLen {
			resp.Rejected = append(resp.Rejected, rejection{in.Ref, "record too large"})
			continue
		}
		// The server cannot read a payload, but it can insist on being unable
		// to: every part must carry the AES-GCM envelope. A client bug that
		// uploaded a note in the clear would be stored forever otherwise, and
		// nothing downstream would ever notice.
		if !in.Deleted && !payloadIsSealed(in.Payload) {
			resp.Rejected = append(resp.Rejected, rejection{in.Ref, "record must arrive encrypted"})
			continue
		}

		var curRev int64
		var curDeleted int
		err := tx.QueryRow("SELECT rev, deleted FROM records WHERE user_id = ? AND ref = ?",
			st.uid, in.Ref).Scan(&curRev, &curDeleted)
		switch {
		case err == sql.ErrNoRows:
			// new record: check it against the plan before it exists. The
			// total cap exists because per-kind limits alone would leave
			// chapters, folders and types unbounded — a hostile client could
			// fill the disk half a megabyte at a time without ever owning
			// "too many notes".
			if counts["*"] >= plan.MaxRecords {
				resp.Rejected = append(resp.Rejected, rejection{in.Ref,
					s.translatef("record limit reached (%d)", plan.MaxRecords)})
				continue
			}
			if over, limit := overQuota(in.Kind, counts, plan); over {
				resp.Rejected = append(resp.Rejected, rejection{in.Ref,
					s.translatef("%s limit reached (%d)", in.Kind, limit)})
				continue
			}
			counts["*"]++
			if !in.Deleted {
				counts[in.Kind]++
			}
		case err != nil:
			resp.Rejected = append(resp.Rejected, rejection{in.Ref, "could not be read"})
			continue
		case in.BaseRev != curRev:
			// somebody else moved it; hand back what is actually stored and
			// let the device decide what to keep
			if server, ok := readRecord(tx, st.uid, in.Ref); ok {
				resp.Conflicts = append(resp.Conflicts, server)
			}
			continue
		case in.Deleted && curDeleted == 0:
			counts[in.Kind]--
		case !in.Deleted && curDeleted == 1:
			counts[in.Kind]++
		}

		seq++
		rev := curRev + 1
		updated := in.UpdatedAt
		// The timestamp arrives from the client (a note written offline is older
		// than its sync), so a past value is legitimate. A future one is not: it
		// would sort ahead of everything indefinitely, so it is pulled back to
		// now. (Cross-account trash deletion is stopped where it belongs — the
		// user_id-scoped purge in db.go — not by trusting this field.) The
		// format is fixed-width, so a lexical compare is a chronological one.
		if !timeShape.MatchString(updated) || updated > now {
			updated = now
		}
		deleted := 0
		if in.Deleted {
			deleted = 1
		}
		_, err = tx.Exec(`INSERT INTO records (user_id, ref, kind, seq, rev, updated_at, deleted, payload)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(user_id, ref) DO UPDATE SET
				seq = excluded.seq, rev = excluded.rev, updated_at = excluded.updated_at,
				deleted = excluded.deleted, payload = excluded.payload`,
			st.uid, in.Ref, in.Kind, seq, rev, updated, deleted, in.Payload)
		if err != nil {
			resp.Rejected = append(resp.Rejected, rejection{in.Ref, "could not be stored"})
			continue
		}
		if deleted == 1 {
			tx.Exec("DELETE FROM blobs WHERE user_id = ? AND ref = ?", st.uid, in.Ref)
		}
		resp.Applied = append(resp.Applied, syncRecord{
			Ref: in.Ref, Kind: in.Kind, Rev: rev, Seq: seq,
			UpdatedAt: updated, Deleted: in.Deleted,
		})
	}

	if err := tx.Commit(); err != nil {
		httpError(w, 500, "could not save the batch")
		return
	}
	resp.Cursor = seq
	writeJSON(w, resp)
}

// payloadIsSealed checks the shape the browser promises: a JSON object whose
// every value is an AES-GCM envelope. It proves nothing about the key, only
// that plaintext is not being handed over by mistake.
func payloadIsSealed(payload string) bool {
	var parts map[string]string
	if err := json.Unmarshal([]byte(payload), &parts); err != nil || len(parts) == 0 {
		return false
	}
	for _, v := range parts {
		// v1 is the original envelope; v2 additionally binds the record's kind
		// and field name as AES-GCM associated data (see static/crypto.js). Both
		// are three dot-separated fields: version, IV, ciphertext.
		if !(strings.HasPrefix(v, "v1.") || strings.HasPrefix(v, "v2.")) || strings.Count(v, ".") != 2 {
			return false
		}
	}
	return true
}

func nextSeq(tx *sql.Tx, uid int64) int64 {
	var seq sql.NullInt64
	tx.QueryRow("SELECT MAX(seq) FROM records WHERE user_id = ?", uid).Scan(&seq)
	return seq.Int64
}

func readRecord(tx *sql.Tx, uid int64, ref string) (syncRecord, bool) {
	var rec syncRecord
	var deleted int
	err := tx.QueryRow(`SELECT ref, kind, rev, seq, updated_at, deleted, payload
		FROM records WHERE user_id = ? AND ref = ?`, uid, ref).
		Scan(&rec.Ref, &rec.Kind, &rec.Rev, &rec.Seq, &rec.UpdatedAt, &deleted, &rec.Payload)
	rec.Deleted = deleted == 1
	return rec, err == nil
}

func liveCounts(tx *sql.Tx, uid int64) map[string]int {
	counts := map[string]int{}
	rows, err := tx.Query(`SELECT kind, count(*) FROM records
		WHERE user_id = ? AND deleted = 0 GROUP BY kind`, uid)
	if err != nil {
		return counts
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var n int
		rows.Scan(&kind, &n)
		counts[kind] = n
		counts["*"] += n
	}
	return counts
}

// overQuota is the one thing the server still judges about the content it
// cannot read: how much of it there is.
func overQuota(kind string, counts map[string]int, p plan) (bool, int) {
	switch kind {
	case "note":
		return counts["note"] >= p.MaxNotes, p.MaxNotes
	case "image":
		return counts["image"] >= p.MaxImages, p.MaxImages
	case "pin":
		// one per device, and nobody locks this many devices: the cap is here
		// so a looping client cannot mint records forever, not to ration them.
		return counts["pin"] >= maxPinRecords, maxPinRecords
	}
	return false, 0
}

/* ---- blobs ---- */

// Image bytes travel on their own so that a first sync is a list of notes
// rather than a download of every picture ever attached to one.
func (s *server) syncBlob(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	if !refShape.MatchString(ref) {
		httpError(w, 400, "malformed address")
		return
	}
	st := s.st(r)
	switch r.Method {
	case http.MethodGet:
		var data []byte
		err := st.db.QueryRow("SELECT data FROM blobs WHERE user_id = ? AND ref = ?",
			st.uid, ref).Scan(&data)
		if err != nil {
			httpError(w, 404, "no such blob")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		// sealed bytes, and their address never changes
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		w.Write(data)
	case http.MethodPut:
		// Bytes are only accepted for an image record this account has already
		// synced. Without that check the blob store would be an unmetered
		// disk: any number of uploads under any number of made-up addresses,
		// none of them counted by the plan.
		var one int
		err := st.db.QueryRow(`SELECT 1 FROM records
			WHERE user_id = ? AND ref = ? AND kind = 'image' AND deleted = 0`,
			st.uid, ref).Scan(&one)
		if err != nil {
			httpError(w, 404, "no image record for this address — sync it first")
			return
		}
		plan := st.plan()
		data, err := io.ReadAll(io.LimitReader(r.Body, plan.MaxImageBytes+1))
		if err != nil {
			httpError(w, 400, "could not read the upload")
			return
		}
		if int64(len(data)) > plan.MaxImageBytes {
			httpError(w, http.StatusRequestEntityTooLarge,
				s.translatef("Image too large: the limit is %d MiB.", plan.MaxImageBytes>>20))
			return
		}
		_, err = st.db.Exec(`INSERT INTO blobs (user_id, ref, data) VALUES (?, ?, ?)
			ON CONFLICT(user_id, ref) DO UPDATE SET data = excluded.data`, st.uid, ref, data)
		if err != nil {
			httpError(w, 500, "could not store the image")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		httpError(w, 405, "method not allowed")
	}
}

/* ---- what the account is using ---- */

func (st *store) usage() usage {
	u := usage{Plan: st.plan()}
	st.db.QueryRow(`SELECT count(*) FROM records
		WHERE user_id = ? AND kind = 'note' AND deleted = 0`, st.uid).Scan(&u.Notes)
	st.db.QueryRow(`SELECT count(*) FROM records
		WHERE user_id = ? AND kind = 'image' AND deleted = 0`, st.uid).Scan(&u.Images)
	u.ImageCap = humanBytes(u.Plan.MaxImageBytes)
	u.NoteCap = humanBytes(int64(u.Plan.MaxChapterBytes))
	return u
}

// syncEnabled reports whether this request may sync at all. Kept as its own
// helper because the answer will get more interesting than "signed in".
func syncPath(p string) bool {
	return p == "/sync" || strings.HasPrefix(p, "/sync/")
}
