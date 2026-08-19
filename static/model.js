"use strict";

/* The decrypted picture of the account, held in memory only.
 *
 * A record on disk is an envelope the server can read (ref, kind, rev, dates)
 * around a payload it cannot. The envelope deliberately does not say which
 * folder a note is in or what it is called: a tree the server could read would
 * describe the account just as well as the notes would. So the shape lives
 * inside the sealed part, and is reassembled here after unlocking.
 *
 * Payloads are JSON with two sealed halves:
 *
 *   h  the header — name, parent, position, metadata. Small, and opened for
 *      every record at load, because the sidebar needs all of them.
 *   b  the body — a chapter's Markdown. Opened only when that chapter is read,
 *      so listing a note does not mean decrypting every word of it.
 *
 * Nothing here is written to disk in the clear, and it is all dropped when the
 * PIN lock wipes the key.
 */

const ngModel = {
	dirs: new Map(),
	notes: new Map(),
	chapters: new Map(),
	types: new Map(),
	images: new Map(),
	loaded: false,
};

/* ---- sealing and opening the two halves ---- */

async function ngSealRecord(header, body) {
	const key = await ngDataKey();
	if (!key) throw new Error(ngT("Locked, sign in again to read this."));
	const out = { h: await ngSeal(key, JSON.stringify(header)) };
	if (body !== undefined && body !== null) out.b = await ngSeal(key, body);
	return JSON.stringify(out);
}

function ngParsePayload(payload) {
	if (!payload) return {};
	try {
		return JSON.parse(payload);
	} catch (err) {
		return {};
	}
}

async function ngOpenHeader(rec) {
	const key = await ngDataKey();
	const parts = ngParsePayload(rec.payload);
	if (!key || !parts.h) return null;
	try {
		return JSON.parse(await ngOpen(key, parts.h));
	} catch (err) {
		return null; // a record this key cannot open is not this account's
	}
}

// ngOpenBody returns a chapter's Markdown, decrypted on demand.
async function ngOpenBody(ref) {
	const rec = await ngGet(ref);
	if (!rec) return "";
	const key = await ngDataKey();
	const parts = ngParsePayload(rec.payload);
	if (!key || !parts.b) return "";
	return ngOpen(key, parts.b);
}

/* ---- loading ---- */

async function ngLoadModel() {
	if (ngLocked()) return ngModel;
	const buckets = {
		dir: ngModel.dirs, note: ngModel.notes, chapter: ngModel.chapters,
		type: ngModel.types, image: ngModel.images,
	};
	Object.values(buckets).forEach(function (m) { m.clear(); });

	for (const kind of Object.keys(buckets)) {
		for (const rec of await ngAllOfKind(kind)) {
			const header = await ngOpenHeader(rec);
			if (!header) continue;
			buckets[kind].set(rec.ref, Object.assign({
				ref: rec.ref,
				rev: rec.rev,
				dirty: !!rec.dirty,
				updated_at: rec.updated_at,
				conflict_of: rec.conflict_of || "",
			}, header));
		}
	}
	ngModel.loaded = true;
	return ngModel;
}

/* ---- reading the shape ---- */

function ngDirChildren(parent) {
	const out = [];
	ngModel.dirs.forEach(function (d) {
		if ((d.parent || "") === (parent || "")) out.push(d);
	});
	return out.sort(ngByName);
}

function ngNotesIn(parent) {
	const out = [];
	ngModel.notes.forEach(function (n) {
		if (!n.trashed && (n.parent || "") === (parent || "")) out.push(n);
	});
	return out.sort(ngByName);
}

function ngTrashedNotes() {
	const out = [];
	ngModel.notes.forEach(function (n) { if (n.trashed) out.push(n); });
	return out.sort(function (a, b) { return (b.trashed || "").localeCompare(a.trashed || ""); });
}

function ngChaptersOf(noteRef) {
	const out = [];
	ngModel.chapters.forEach(function (c) {
		if (c.note === noteRef) out.push(c);
	});
	return out.sort(function (a, b) { return (a.pos || 0) - (b.pos || 0); });
}

function ngImagesOf(noteRef) {
	const out = [];
	ngModel.images.forEach(function (i) {
		if (i.note === noteRef) out.push(i);
	});
	return out;
}

function ngByName(a, b) {
	return String(a.name || a.title || "").localeCompare(String(b.name || b.title || ""),
		undefined, { sensitivity: "base" });
}

/* ---- changing it ----
 *
 * Each of these writes IndexedDB and updates the in-memory copy, then lets the
 * views redraw. None of them waits for a network.
 */

async function ngCreateDir(name, parent) {
	const header = { name: name, parent: parent || "" };
	const rec = await ngCreate("dir", parent || "", await ngSealRecord(header));
	ngModel.dirs.set(rec.ref, Object.assign({ ref: rec.ref, rev: 0 }, header));
	return rec.ref;
}

async function ngCreateNote(title, parent, type) {
	const header = { title: title, parent: parent || "", type: type || "", meta: {}, description: "" };
	const rec = await ngCreate("note", parent || "", await ngSealRecord(header));
	ngModel.notes.set(rec.ref, Object.assign({ ref: rec.ref, rev: 0 }, header));
	// a note with no chapters has nowhere to write, so it starts with one
	await ngCreateChapter(rec.ref, ngT("Chapter 1"));
	return rec.ref;
}

async function ngCreateChapter(noteRef, title) {
	const pos = ngChaptersOf(noteRef).length + 1;
	const header = { title: title, note: noteRef, pos: pos };
	const rec = await ngCreate("chapter", noteRef, await ngSealRecord(header, ""));
	ngModel.chapters.set(rec.ref, Object.assign({ ref: rec.ref, rev: 0 }, header));
	return rec.ref;
}

async function ngCreateType(name, fields) {
	const header = { name: name, fields: fields || [] };
	const rec = await ngCreate("type", "", await ngSealRecord(header));
	ngModel.types.set(rec.ref, Object.assign({ ref: rec.ref, rev: 0 }, header));
	return rec.ref;
}

// ngUpdate rewrites one record's header, and its body when one is given.
async function ngUpdate(kind, ref, changes, body) {
	const bucket = ngBucket(kind);
	const item = bucket.get(ref);
	if (!item) return null;
	const merged = Object.assign({}, item, changes);
	const header = {};
	Object.keys(merged).forEach(function (k) {
		if (k !== "ref" && k !== "rev" && k !== "dirty" && k !== "updated_at" && k !== "conflict_of") {
			header[k] = merged[k];
		}
	});
	const rec = await ngGetAny(ref);
	if (!rec) return null;
	// keep the existing body unless a new one is supplied
	let newBody = body;
	if (newBody === undefined) {
		const parts = ngParsePayload(rec.payload);
		rec.payload = JSON.stringify(Object.assign(ngParsePayload(await ngSealRecord(header)),
			parts.b ? { b: parts.b } : {}));
	} else {
		rec.payload = await ngSealRecord(header, newBody);
	}
	rec.parent = header.parent || header.note || "";
	await ngPut(rec);
	bucket.set(ref, merged);
	return merged;
}

function ngBucket(kind) {
	return {
		dir: ngModel.dirs, note: ngModel.notes, chapter: ngModel.chapters,
		type: ngModel.types, image: ngModel.images,
	}[kind];
}

// Trashing is not deleting: a note keeps its records so it can come back, and
// only the purge that follows leaves tombstones.
async function ngTrashNote(ref) {
	await ngUpdate("note", ref, { trashed: ngNow() });
}

async function ngRestoreNote(ref) {
	await ngUpdate("note", ref, { trashed: "" });
}

async function ngPurgeNote(ref) {
	for (const c of ngChaptersOf(ref)) {
		await ngDelete(c.ref);
		ngModel.chapters.delete(c.ref);
	}
	for (const i of ngImagesOf(ref)) {
		await ngDelete(i.ref);
		ngModel.images.delete(i.ref);
	}
	await ngDelete(ref);
	ngModel.notes.delete(ref);
}

async function ngDeleteDir(ref) {
	// folders only disappear when nothing is left in them, so nothing is
	// removed by surprise
	if (ngDirChildren(ref).length || ngNotesIn(ref).length) return false;
	await ngDelete(ref);
	ngModel.dirs.delete(ref);
	return true;
}

async function ngDeleteChapter(ref) {
	const chapter = ngModel.chapters.get(ref);
	if (!chapter) return;
	await ngDelete(ref);
	ngModel.chapters.delete(ref);
	await ngRenumber(chapter.note);
}

async function ngRenumber(noteRef) {
	const chapters = ngChaptersOf(noteRef);
	for (let i = 0; i < chapters.length; i++) {
		if (chapters[i].pos !== i + 1) await ngUpdate("chapter", chapters[i].ref, { pos: i + 1 });
	}
}

async function ngMoveChapter(ref, direction) {
	const chapter = ngModel.chapters.get(ref);
	if (!chapter) return;
	const chapters = ngChaptersOf(chapter.note);
	const at = chapters.findIndex(function (c) { return c.ref === ref; });
	const to = direction === "up" ? at - 1 : at + 1;
	if (to < 0 || to >= chapters.length) return;
	const other = chapters[to];
	await ngUpdate("chapter", ref, { pos: other.pos });
	await ngUpdate("chapter", other.ref, { pos: chapter.pos });
}

/* ---- what a new account starts with ---- */

async function ngSeedDefaults() {
	if (ngModel.types.size) return;
	await ngCreateType(ngT("Note"), []);
	await ngCreateType(ngT("Book record"), [
		{ key: "author", label: ngT("Author"), type: "text" },
		{ key: "edition", label: ngT("Edition"), type: "text" },
		{ key: "year", label: ngT("Year"), type: "number" },
		{ key: "finished", label: ngT("Finished"), type: "date" },
	]);
}
