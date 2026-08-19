"use strict";

/* Catching the server up, and catching up with it.
 *
 * Sync is never on the path of anything a person does. Writing a note puts it
 * in IndexedDB and returns; this runs afterwards, and if it fails — no signal,
 * server down, laptop shut — nothing is lost and nothing is blocked. That is
 * the whole design: the network is an optimisation, not a dependency.
 *
 * A round is: push what is dirty, then pull what is new. Push first, so that a
 * record edited here is not overwritten by the copy the server is about to
 * hand back.
 *
 * Conflicts are real and are not resolved by guessing. When the server refuses
 * a write because the record moved underneath us, the remote version wins the
 * address and the local version is kept as a new record beside it. Nothing is
 * silently thrown away; the reader is told, and decides.
 */

const NG_SYNC_BATCH = 100;
const NG_SYNC_IDLE_MS = 30000;
const NG_CURSOR_KEY = "cursor";

let ngSyncing = false;
let ngSyncQueued = false;
let ngSyncTimer = null;
let ngLastError = "";

function ngOnline() {
	return navigator.onLine !== false;
}

/* ---- status, so the interface can be honest about what is stored where ---- */

const ngSyncListeners = [];

function ngOnSyncChange(fn) {
	ngSyncListeners.push(fn);
}

async function ngSyncState() {
	const dirty = await ngDirtyRecords();
	const blobs = await ngDirtyBlobs();
	return {
		online: ngOnline(),
		syncing: ngSyncing,
		pending: dirty.length + blobs.length,
		error: ngLastError,
		cursor: await ngMeta(NG_CURSOR_KEY, 0),
	};
}

async function ngAnnounceSync() {
	const state = await ngSyncState();
	ngSyncListeners.forEach(function (fn) {
		try { fn(state); } catch (err) { /* a broken listener must not stop sync */ }
	});
}

/* ---- the round ---- */

// ngNudgeSync is called by every local write. It coalesces: a burst of edits
// produces one round, not one per keystroke.
function ngNudgeSync() {
	if (ngSyncTimer) clearTimeout(ngSyncTimer);
	ngSyncTimer = setTimeout(function () { ngSync(); }, 800);
	ngAnnounceSync();
}

async function ngSync() {
	if (ngSyncing) {
		ngSyncQueued = true;
		return;
	}
	if (!ngOnline() || ngLocked()) return;
	ngSyncing = true;
	ngAnnounceSync();
	try {
		await ngPushBlobs();
		await ngPush();
		await ngPull();
		ngLastError = "";
	} catch (err) {
		// Offline, signed out, server restarting: all the same here. The work
		// is still in IndexedDB and the next round will carry it.
		ngLastError = String((err && err.message) || err);
	} finally {
		ngSyncing = false;
		await ngAnnounceSync();
		if (ngSyncQueued) {
			ngSyncQueued = false;
			ngSync();
		}
	}
}

async function ngPush() {
	let dirty = await ngDirtyRecords(NG_SYNC_BATCH);
	while (dirty.length) {
		const body = {
			records: dirty.map(function (rec) {
				return {
					ref: rec.ref,
					kind: rec.kind,
					base_rev: rec.rev || 0,
					updated_at: rec.updated_at,
					deleted: !!rec.deleted,
					payload: rec.payload || "",
				};
			}),
		};
		const resp = await fetch("/sync", {
			method: "POST",
			headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken() },
			body: JSON.stringify(body),
		});
		if (!resp.ok) throw new Error("push failed: " + resp.status);
		const result = await resp.json();

		for (const rec of result.applied) {
			await ngMarkClean(rec.ref, rec.rev, rec.seq);
		}
		for (const remote of result.conflicts) {
			await ngResolveConflict(remote);
		}
		for (const bad of result.rejected) {
			// Refused for a reason that will not change by retrying — a plan
			// limit, an oversized record. Stop calling it dirty, and say so.
			await ngMarkClean(bad.ref, 0, 0);
			ngLastError = bad.reason;
		}
		if (!result.applied.length && !result.conflicts.length) break;
		dirty = await ngDirtyRecords(NG_SYNC_BATCH);
	}
}

// ngResolveConflict keeps both sides. The server's version takes the address,
// because that is what every other device already agrees on; the local edit
// becomes a new record next to it, flagged so the interface can point at it.
async function ngResolveConflict(remote) {
	const local = await ngGetAny(remote.ref);
	if (local && !remote.deleted && local.payload && local.payload !== remote.payload) {
		await ngCreate(local.kind, local.parent, local.payload).then(function (copy) {
			copy.conflict_of = remote.ref;
			return ngPut(copy);
		});
	}
	await ngApplyRemoteForce(remote);
}

async function ngApplyRemoteForce(remote) {
	const tx = await ngTx(["records"], "readwrite");
	await ngReq(tx.objectStore("records").put({
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
}

async function ngPull() {
	let cursor = await ngMeta(NG_CURSOR_KEY, 0);
	for (;;) {
		const resp = await fetch("/sync?since=" + encodeURIComponent(cursor), {
			headers: { "X-Requested-With": "neuroscribe" },
		});
		if (!resp.ok) throw new Error("pull failed: " + resp.status);
		const page = await resp.json();
		for (const rec of page.records) {
			await ngApplyRemote(rec);
		}
		cursor = page.cursor;
		await ngSetMeta(NG_CURSOR_KEY, cursor);
		if (page.records.length) ngNotifyData();
		if (!page.more) break;
	}
	// Proof that this device has seen everything the account holds. Until it
	// exists, "there are no note types" means "nothing has arrived yet", not
	// "this account is new" — seeding on the first reading is how a second
	// device ends up with two copies of every default.
	await ngSetMeta("pulled-once", true);
}

/* ---- image bytes ---- */

async function ngPushBlobs() {
	const pending = await ngDirtyBlobs();
	for (const blob of pending) {
		const resp = await fetch("/sync/blob/" + encodeURIComponent(blob.ref), {
			method: "PUT",
			headers: { "Content-Type": "application/octet-stream", "X-CSRF-Token": csrfToken() },
			body: blob.data,
		});
		if (!resp.ok) throw new Error("image upload failed: " + resp.status);
		await ngPutBlob(blob.ref, blob.data, false);
	}
}

// ngFetchBlob returns image bytes, from here if they are here and from the
// server if they are not. A note opened for the first time on a new device
// pulls its pictures the moment it is read, and never again.
async function ngFetchBlob(ref) {
	const local = await ngGetBlob(ref);
	if (local) return local;
	if (!ngOnline()) return null;
	const resp = await fetch("/sync/blob/" + encodeURIComponent(ref));
	if (!resp.ok) return null;
	const bytes = new Uint8Array(await resp.arrayBuffer());
	await ngPutBlob(ref, bytes, false);
	return bytes;
}

/* ---- telling the page something changed ---- */

const ngDataListeners = [];

function ngOnData(fn) {
	ngDataListeners.push(fn);
}

function ngNotifyData() {
	ngDataListeners.forEach(function (fn) {
		try { fn(); } catch (err) { /* one bad view must not stop the others */ }
	});
}

/* ---- when to run ---- */

function ngStartSync() {
	if (!document.body || !document.body.dataset.app) return;
	window.addEventListener("online", function () { ngLastError = ""; ngSync(); });
	window.addEventListener("offline", ngAnnounceSync);
	document.addEventListener("visibilitychange", function () {
		if (!document.hidden) ngSync();
	});
	setInterval(function () { ngSync(); }, NG_SYNC_IDLE_MS);
	ngSync();
}
