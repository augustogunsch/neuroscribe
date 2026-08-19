"use strict";

/* The local database.
 *
 * This is where the notes are. Not a cache of the server's copy — the copy.
 * Everything the app reads comes from here, everything it writes lands here
 * first, and the server finds out later (see sync.js). That ordering is what
 * makes the app work with the network off: there is no request to fail.
 *
 * Records are stored exactly as they travel: sealed payloads with a plain
 * envelope of ref, kind, rev and timestamps. IndexedDB is not encrypted at
 * rest, so nothing readable is written here — the sealed payload is opened
 * only in memory, only while unlocked.
 *
 * Three fields drive synchronisation:
 *
 *   rev      what the server last confirmed. A record edited locally keeps the
 *            rev it was based on, which is how a stale write is detected.
 *   dirty    changed here and not yet accepted by the server.
 *   deleted  a tombstone. Deleting must be a record too, or a device that was
 *            offline would resurrect what another device removed.
 */

const NG_DB_NAME = "neuroscribe";
const NG_DB_VERSION = 1;
const NG_STORES = ["records", "blobs", "meta"];

let ngDbHandle = null;

function ngDB() {
	if (ngDbHandle) return ngDbHandle;
	ngDbHandle = new Promise(function (resolve, reject) {
		const req = indexedDB.open(NG_DB_NAME, NG_DB_VERSION);
		req.onupgradeneeded = function () {
			const db = req.result;
			if (!db.objectStoreNames.contains("records")) {
				const records = db.createObjectStore("records", { keyPath: "ref" });
				records.createIndex("kind", "kind");
				records.createIndex("dirty", "dirty");
				// chapters by note, notes by folder: the two lookups every page makes
				records.createIndex("parent", "parent");
			}
			if (!db.objectStoreNames.contains("blobs")) {
				db.createObjectStore("blobs", { keyPath: "ref" });
			}
			if (!db.objectStoreNames.contains("meta")) {
				db.createObjectStore("meta", { keyPath: "key" });
			}
		};
		req.onsuccess = function () { resolve(req.result); };
		req.onerror = function () { reject(req.error); };
	});
	return ngDbHandle;
}

function ngTx(stores, mode) {
	return ngDB().then(function (db) {
		return db.transaction(stores, mode);
	});
}

function ngReq(request) {
	return new Promise(function (resolve, reject) {
		request.onsuccess = function () { resolve(request.result); };
		request.onerror = function () { reject(request.error); };
	});
}

/* ---- addresses ----
 *
 * Minted here rather than by the server, because a note written offline needs
 * an address before anything has heard of it. Nine random bytes: short enough
 * for a URL, far too large a space to collide in one account.
 */

function ngNewRef() {
	const bytes = crypto.getRandomValues(new Uint8Array(9));
	let s = "";
	for (let i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
	return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function ngNow() {
	return new Date().toISOString().slice(0, 19).replace("T", " ");
}

/* ---- reading ---- */

async function ngGet(ref) {
	const tx = await ngTx(["records"], "readonly");
	const rec = await ngReq(tx.objectStore("records").get(ref));
	return rec && !rec.deleted ? rec : null;
}

// ngGetAny includes tombstones — sync needs them, pages do not.
async function ngGetAny(ref) {
	const tx = await ngTx(["records"], "readonly");
	return (await ngReq(tx.objectStore("records").get(ref))) || null;
}

async function ngAllOfKind(kind, includeDeleted) {
	const tx = await ngTx(["records"], "readonly");
	const rows = await ngReq(tx.objectStore("records").index("kind").getAll(kind));
	return includeDeleted ? rows : rows.filter(function (r) { return !r.deleted; });
}

async function ngChildrenOf(parent, kind) {
	const tx = await ngTx(["records"], "readonly");
	const rows = await ngReq(tx.objectStore("records").index("parent").getAll(parent || ""));
	return rows.filter(function (r) {
		return !r.deleted && (!kind || r.kind === kind);
	});
}

/* ---- writing ----
 *
 * Every local write marks the record dirty and bumps nothing else: rev stays
 * at whatever the server last confirmed, because that is the number the server
 * needs in order to tell whether this edit was based on current information.
 */

async function ngPut(rec) {
	rec.updated_at = ngNow();
	rec.dirty = 1;
	rec.parent = rec.parent || "";
	const tx = await ngTx(["records"], "readwrite");
	await ngReq(tx.objectStore("records").put(rec));
	ngNudgeSync();
	return rec;
}

async function ngCreate(kind, parent, payload) {
	return ngPut({
		ref: ngNewRef(),
		kind: kind,
		parent: parent || "",
		payload: payload,
		rev: 0, // never seen by the server
		seq: 0,
		deleted: 0,
	});
}

// ngDelete leaves a tombstone. The record keeps its rev so the server can tell
// whether the deletion was based on the current version.
async function ngDelete(ref) {
	const rec = await ngGetAny(ref);
	if (!rec) return;
	rec.deleted = 1;
	rec.payload = "";
	await ngPut(rec);
}

// ngPurgeLocal removes a record outright, used only for records the server has
// confirmed gone. Everything a person deletes goes through ngDelete.
async function ngPurgeLocal(ref) {
	const tx = await ngTx(["records", "blobs"], "readwrite");
	tx.objectStore("records").delete(ref);
	tx.objectStore("blobs").delete(ref);
}

/* ---- records the server has confirmed ---- */

// ngApplyRemote writes a record that came from the server. A local edit that
// has not been pushed yet wins for now and stays dirty; sync.js resolves it
// against the server on the next push.
async function ngApplyRemote(remote) {
	const tx = await ngTx(["records"], "readwrite");
	const store = tx.objectStore("records");
	const local = await ngReq(store.get(remote.ref));
	if (local && local.dirty) return "kept-local";
	await ngReq(store.put({
		ref: remote.ref,
		kind: remote.kind,
		parent: remote.parent || "",
		payload: remote.payload || "",
		rev: remote.rev,
		seq: remote.seq,
		updated_at: remote.updated_at,
		deleted: remote.deleted ? 1 : 0,
		dirty: 0,
	}));
	return "applied";
}

// ngMarkClean records what the server accepted: the new rev is now the base
// for the next edit.
async function ngMarkClean(ref, rev, seq) {
	const tx = await ngTx(["records"], "readwrite");
	const store = tx.objectStore("records");
	const rec = await ngReq(store.get(ref));
	if (!rec) return;
	rec.rev = rev;
	rec.seq = seq;
	rec.dirty = 0;
	await ngReq(store.put(rec));
}

async function ngDirtyRecords(limit) {
	const tx = await ngTx(["records"], "readonly");
	const rows = await ngReq(tx.objectStore("records").index("dirty").getAll(1));
	return limit ? rows.slice(0, limit) : rows;
}

/* ---- blobs ----
 *
 * Image bytes, kept out of the record stream so a first sync is a list of
 * notes rather than a download of every picture.
 */

async function ngPutBlob(ref, bytes, dirty) {
	const tx = await ngTx(["blobs"], "readwrite");
	await ngReq(tx.objectStore("blobs").put({ ref: ref, data: bytes, dirty: dirty ? 1 : 0 }));
}

async function ngGetBlob(ref) {
	const tx = await ngTx(["blobs"], "readonly");
	const row = await ngReq(tx.objectStore("blobs").get(ref));
	return row ? row.data : null;
}

async function ngDirtyBlobs() {
	const tx = await ngTx(["blobs"], "readonly");
	const rows = await ngReq(tx.objectStore("blobs").getAll());
	return rows.filter(function (r) { return r.dirty; });
}

/* ---- small facts about this device ---- */

async function ngMeta(key, def) {
	const tx = await ngTx(["meta"], "readonly");
	const row = await ngReq(tx.objectStore("meta").get(key));
	return row === undefined || row === null ? def : row.value;
}

async function ngSetMeta(key, value) {
	const tx = await ngTx(["meta"], "readwrite");
	await ngReq(tx.objectStore("meta").put({ key: key, value: value }));
}

// ngWipeLocal clears everything this device holds. Used when signing out: the
// records are sealed, but a shared machine has no business keeping them.
async function ngWipeLocal() {
	const tx = await ngTx(NG_STORES, "readwrite");
	NG_STORES.forEach(function (name) { tx.objectStore(name).clear(); });
	return new Promise(function (resolve) { tx.oncomplete = resolve; });
}
