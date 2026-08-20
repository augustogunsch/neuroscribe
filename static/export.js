"use strict";

/* Getting your writing out.
 *
 * Both exports are built from this device's own copy, which means they work
 * with no connection — and, more to the point, that no plaintext is ever
 * assembled anywhere but here.
 */

// ngDownload hands a finished file to whoever is hosting the page.
//
// In a browser that is an <a download> pointed at an object URL. In the Android
// app it cannot be: a WebView does not download blob: URLs — the click is
// simply ignored, with no error anywhere — so a PDF export would appear to work
// and produce nothing. The app supplies a way through instead, and the bytes go
// over it in chunks because the bridge between a page and its host is not a
// place to put a whole document in one call.
async function ngDownload(blob, name) {
	const native = window.NeuroscribeNative;
	if (native && typeof native.saveFile === "function") {
		const bytes = new Uint8Array(await blob.arrayBuffer());
		const chunk = 256 * 1024;
		for (let at = 0; at < bytes.length || at === 0; at += chunk) {
			const slice = bytes.subarray(at, at + chunk);
			let binary = "";
			for (let i = 0; i < slice.length; i++) binary += String.fromCharCode(slice[i]);
			native.saveFile(name, blob.type || "application/octet-stream",
				btoa(binary), at === 0, at + chunk >= bytes.length);
		}
		return;
	}
	const url = URL.createObjectURL(blob);
	const a = document.createElement("a");
	a.href = url;
	a.download = name;
	document.body.appendChild(a);
	a.click();
	a.remove();
	setTimeout(function () { URL.revokeObjectURL(url); }, 10000);
}

function ngSafeName(title) {
	const clean = String(title || "").replace(/[\\/:*?"<>|]+/g, "_").replace(/\.+$/, "").trim();
	return clean || "untitled";
}

// ngNoteForExport assembles one note the way both exporters want it: fully
// decrypted, chapters in order, metadata resolved against its type.
async function ngNoteForExport(ref) {
	const note = ngModel.notes.get(ref);
	if (!note) return null;
	const type = ngModel.types.get(note.type);
	const chapters = [];
	for (const c of ngChaptersOf(ref)) {
		chapters.push({ title: c.title, content: await ngOpenBody(c.ref) });
	}
	return {
		ref: ref,
		title: note.title,
		type: type ? type.name : "",
		fields: (type && type.fields) || [],
		meta: note.meta || {},
		description: note.description || "",
		chapters: chapters,
	};
}

/* ---- one note, typeset ---- */

async function ngExportNotePDF(ref) {
	const note = await ngNoteForExport(ref);
	if (!note) return;
	try {
		const pdf = await ngNotePDF(note, {
			date: new Date().toISOString().slice(0, 10),
			contents: ngT("Contents"),
		});
		await ngDownload(new Blob([pdf], { type: "application/pdf" }), ngSafeName(note.title) + ".pdf");
	} catch (err) {
		ngToast(ngT("The typesetter could not build this note.") + " " + String((err && err.message) || err));
	}
}

/* ---- everything, as a zip ---- */

async function ngExportEverything(button) {
	const label = button ? button.textContent : "";
	if (button) {
		button.disabled = true;
		button.textContent = ngT("Preparing…");
	}
	try {
		const entries = [];
		const seen = new Set();
		// folder paths are reconstructed here because only this device knows
		// them: the server stores the tree sealed inside the records
		const pathOf = function (dirRef) {
			const parts = [];
			let cur = ngModel.dirs.get(dirRef);
			let guard = 0;
			while (cur && guard++ < 64) {
				parts.unshift(ngSafeName(cur.name));
				cur = ngModel.dirs.get(cur.parent);
			}
			return parts.join("/");
		};

		for (const note of ngModel.notes.values()) {
			if (note.trashed) continue;
			const full = await ngNoteForExport(note.ref);
			if (!full) continue;
			const dir = pathOf(note.parent);
			let name = (dir ? dir + "/" : "") + ngSafeName(note.title) + ".md";
			let n = 2;
			while (seen.has(name)) {
				name = (dir ? dir + "/" : "") + ngSafeName(note.title) + "-" + n++ + ".md";
			}
			seen.add(name);

			const md = [];
			md.push("# " + full.title + "\n");
			if (full.description) md.push("> " + full.description + "\n");
			full.fields.forEach(function (f) {
				if (full.meta[f.key]) md.push("- **" + f.label + ":** " + full.meta[f.key]);
			});
			full.chapters.forEach(function (c) {
				md.push("\n## " + c.title + "\n\n" + c.content);
			});
			entries.push({ name: name, data: md.join("\n") });
		}

		// images, decrypted, under the addresses the Markdown refers to
		const key = await ngDataKey();
		for (const img of ngModel.images.values()) {
			const sealed = await ngFetchBlob(img.ref);
			if (!sealed || !key) continue;
			try {
				entries.push({
					name: "_images/" + img.ref + ngImageExt(img.mime),
					data: await ngOpenBytes(key, sealed),
				});
			} catch (err) { /* an image that will not open is left out */ }
		}

		await ngDownload(await ngZip(entries), "neuroscribe-export.zip");
	} finally {
		if (button) {
			button.disabled = false;
			button.textContent = label;
		}
	}
}

function ngImageExt(mime) {
	return {
		"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif",
		"image/webp": ".webp", "image/svg+xml": ".svg",
	}[mime] || ".bin";
}
