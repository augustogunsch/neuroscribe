"use strict";

/* Which page to draw, and the pieces the pages share.
 *
 * Addresses are still real addresses — /notes/<ref>/<ref> can be bookmarked,
 * shared between your own devices and opened cold. The difference is that the
 * server answers every one of them with the same shell, and this decides what
 * goes in it by reading the local store.
 */

function ngTF(format) {
	const args = Array.prototype.slice.call(arguments, 1);
	let i = 0;
	return ngT(format).replace(/%s|%d/g, function () { return args[i++]; });
}

/* ---- routing ---- */

function ngRoute() {
	const parts = location.pathname.split("/").filter(Boolean);
	if (!parts.length) return { name: "index" };
	switch (parts[0]) {
		case "notes":
			if (parts.length >= 3) return { name: "chapter", note: parts[1], chapter: parts[2] };
			if (parts.length === 2) return { name: "note", note: parts[1] };
			return { name: "index" };
		case "trash": return { name: "trash" };
		case "settings": return { name: "settings" };
		case "types": return { name: "types" };
		default: return { name: "missing" };
	}
}

let ngRendering = false;

async function ngRender() {
	if (ngRendering) return;
	ngRendering = true;
	try {
		if (ngLocked()) return; // the lock screen is up; nothing to draw behind it
		if (!ngModel.loaded) await ngLoadModel();
		ngRenderTree();
		const route = ngRoute();
		switch (route.name) {
			case "index": await ngViewIndex(); break;
			case "note": await ngViewNote(route.note); break;
			case "chapter": await ngViewChapter(route.note, route.chapter); break;
			case "trash": await ngViewTrash(); break;
			case "settings": await ngViewSettings(); break;
			case "types": await ngViewTypes(); break;
			default: ngViewMissing();
		}
	} finally {
		ngRendering = false;
	}
}

function ngNavigate(path) {
	if (path !== location.pathname) history.pushState({}, "", path);
	ngRender();
}

/* ---- the dialog ----
 *
 * One <dialog class="modal"> in the shell, driven here. showModal() brings a
 * real backdrop, focus trapping and Escape for free — everything a floating
 * div has to fake and usually fakes badly.
 */

function ngDialog(opts) {
	return new Promise(function (resolve) {
		const dlg = document.getElementById("app-dialog");
		if (!dlg || typeof dlg.showModal !== "function") {
			resolve(null);
			return;
		}
		const title = document.getElementById("app-dialog-title");
		const input = document.getElementById("app-dialog-input");
		const ok = document.getElementById("app-dialog-ok");
		const cancel = document.getElementById("app-dialog-cancel");

		title.textContent = opts.title || "";
		ok.textContent = opts.okLabel || ngT("OK");
		cancel.textContent = ngT("Cancel");
		ok.classList.toggle("danger", !!opts.danger);
		ok.classList.toggle("primary", !opts.danger);
		if (opts.input) {
			input.hidden = false;
			input.required = true;
			input.value = opts.value || "";
		} else {
			input.hidden = true;
			input.required = false;
			input.value = "";
		}

		function onCancel() { dlg.close("cancel"); }
		function onClose() {
			dlg.removeEventListener("close", onClose);
			cancel.removeEventListener("click", onCancel);
			if (dlg.returnValue !== "ok") { resolve(null); return; }
			resolve(opts.input ? input.value.trim() : true);
		}
		dlg.addEventListener("close", onClose);
		cancel.addEventListener("click", onCancel);
		dlg.returnValue = "";
		dlg.showModal();
		if (opts.input) { input.focus(); input.select(); }
	});
}

function ngAsk(title, value) {
	return ngDialog({ title: title, input: true, value: value });
}

function ngConfirm(question, danger) {
	return ngDialog({ title: question, danger: danger }).then(function (v) { return v === true; });
}

/* ---- toasts ---- */

function ngToast(message) {
	const host = document.getElementById("toasts");
	if (!host) return;
	const toast = ngEl("div", { class: "toast", text: message });
	host.appendChild(toast);
	setTimeout(function () { toast.remove(); }, 4000);
}

/* ---- the things the tree and the note page ask for ---- */

async function ngNewNotePrompt(parent) {
	const title = await ngAsk(ngT("Note title"), "");
	if (!title) return;
	const ref = await ngCreateNote(title, parent || "");
	ngNavigate("/notes/" + ref);
}

async function ngNewDirPrompt(parent) {
	const name = await ngAsk(ngT("Folder name"), "");
	if (!name) return;
	await ngCreateDir(name, parent || "");
	ngRender();
}

async function ngRenameDirPrompt(dir) {
	const name = await ngAsk(ngT("New folder name"), dir.name);
	if (!name) return;
	await ngUpdate("dir", dir.ref, { name: name });
	ngRender();
}

async function ngDeleteDirPrompt(dir) {
	if (ngDirChildren(dir.ref).length || ngNotesIn(dir.ref).length) {
		ngToast(ngT("Empty the folder before deleting it."));
		return;
	}
	if (!(await ngConfirm(ngT("Delete this folder?"), true))) return;
	await ngDeleteDir(dir.ref);
	ngRender();
}

async function ngRenameNotePrompt(note) {
	const title = await ngAsk(ngT("New note title"), note.title);
	if (!title) return;
	await ngUpdate("note", note.ref, { title: title });
	ngRender();
}

async function ngRenameChapterPrompt(chapter) {
	const title = await ngAsk(ngT("Chapter title"), chapter.title);
	if (!title) return;
	await ngUpdate("chapter", chapter.ref, { title: title });
	ngRender();
}

// The keyboard-and-touch counterpart of dragging a note onto a folder: the
// same one-field move, through an explicit picker.
async function ngMoveNotePrompt(note) {
	const options = [{ ref: "", name: ngT("(top level)") }]
		.concat(Array.from(ngModel.dirs.values()).sort(ngByName));
	const select = ngEl("select", {}, options.map(function (d) {
		return ngEl("option", { value: d.ref, text: d.name, selected: d.ref === (note.parent || "") });
	}));
	const host = ngViewHost();
	host.replaceChildren(
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Move note") })]),
		ngEl("div", { class: "meta-card editing" }, [
			ngEl("form", { onsubmit: async function (e) {
				e.preventDefault();
				await ngUpdate("note", note.ref, { parent: select.value });
				ngNavigate("/notes/" + note.ref);
			} }, [
				ngEl("label", { class: "meta-label", text: ngT("Folder") + " " }, [select]),
				ngEl("div", { class: "edit-actions" }, [
					ngEl("button", { type: "submit", class: "primary", text: ngT("Move") }),
					ngEl("button", { type: "button", text: ngT("Cancel"), onclick: function () { ngRender(); } }),
				]),
			]),
		]),
	);
}

// The metadata editor takes over the meta-card in place rather than
// replacing the whole page.
async function ngEditMeta(note) {
	const type = ngModel.types.get(note.type);
	const fields = (type && type.fields) || [];
	const inputs = {};

	const typeSelect = ngEl("select", {}, Array.from(ngModel.types.values()).sort(ngByName).map(function (t) {
		return ngEl("option", { value: t.ref, text: t.name, selected: t.ref === note.type });
	}));
	const description = ngEl("textarea", { rows: "3" });
	description.value = note.description || "";

	const rows = fields.map(function (f) {
		const input = f.type === "checkbox"
			? ngEl("input", { type: "checkbox", checked: !!(note.meta && note.meta[f.key]) })
			: ngEl("input", {
				type: f.type === "number" ? "number" : f.type === "date" ? "date" : f.type === "url" ? "url" : "text",
				value: (note.meta && note.meta[f.key]) || "",
			});
		inputs[f.key] = { el: input, kind: f.type };
		return ngEl("label", { class: "meta-label", text: f.label + " " }, [input]);
	});

	const card = document.querySelector(".meta-card") || ngViewHost();
	card.classList.add("editing");
	card.replaceChildren(ngEl("form", { onsubmit: async function (e) {
		e.preventDefault();
		const meta = {};
		Object.entries(inputs).forEach(function ([key, entry]) {
			if (entry.kind === "checkbox") {
				if (entry.el.checked) meta[key] = "yes";
			} else if (entry.el.value) {
				meta[key] = entry.el.value;
			}
		});
		await ngUpdate("note", note.ref, {
			type: typeSelect.value, description: description.value, meta: meta,
		});
		ngRender();
	} }, [
		ngEl("label", { class: "meta-label", text: ngT("Type") + " " }, [typeSelect]),
		ngEl("label", { class: "meta-label", text: ngT("Description") + " " }, [description]),
	].concat(rows).concat([
		ngEl("div", { class: "edit-actions" }, [
			ngEl("button", { type: "submit", class: "primary", text: ngT("Save") }),
			ngEl("button", { type: "button", text: ngT("Cancel"), onclick: function () { ngRender(); } }),
		]),
	])));
	// switching type mid-edit changes which fields exist: save the choice and
	// reopen the editor so the right fields appear
	typeSelect.addEventListener("change", async function () {
		await ngUpdate("note", note.ref, { type: typeSelect.value });
		await ngRender();
		ngEditMeta(ngModel.notes.get(note.ref));
	});
}

/* ---- images ---- */

async function ngUploadImage(input, noteRef) {
	const file = input.files[0];
	if (!file) return;
	const key = await ngDataKey();
	if (!key) return;
	const sealed = await ngSealBytes(key, new Uint8Array(await file.arrayBuffer()));
	const header = { name: file.name, mime: file.type, note: noteRef };
	const rec = await ngCreate("image", noteRef, await ngSealRecord("image", header));
	await ngPutBlob(rec.ref, sealed, true);
	ngModel.images.set(rec.ref, Object.assign({ ref: rec.ref, rev: 0 }, header));
	input.value = "";
	ngRender();
}

// ngPaintImage decrypts one image into an object URL. Bytes come from this
// device when they are here and from the server the first time they are not.
async function ngPaintImage(el, ref) {
	const key = await ngDataKey();
	const sealed = await ngFetchBlob(ref);
	if (!key || !sealed) return;
	try {
		const plain = await ngOpenBytes(key, sealed);
		const meta = ngModel.images.get(ref) || {};
		el.src = URL.createObjectURL(new Blob([plain], { type: meta.mime || "application/octet-stream" }));
	} catch (err) { /* an image that will not open is left blank */ }
}

// ngShowImages resolves the /images/<ref> links Markdown carries.
async function ngShowImages(scope) {
	const imgs = scope.querySelectorAll('img[src^="/images/"]:not([data-shown])');
	for (const img of imgs) {
		img.dataset.shown = "1";
		await ngPaintImage(img, img.getAttribute("src").slice("/images/".length));
	}
}

/* ---- the editor toolbar ---- */

function ngMdToolbar(area, noteRef) {
	const actions = [
		["bold", "B", ngT("Bold")], ["italic", "I", ngT("Italic")], ["strike", "S̶", ngT("Strikethrough")],
		null,
		["heading", "H", ngT("Heading")], ["list", "•", ngT("List")], ["quote", "❝", ngT("Quote")],
		null,
		["code", "</>", ngT("Code")], ["codeblock", "{ }", ngT("Code block")], ["math", "∑", ngT("Math")],
		["link", "🔗", ngT("Link")],
	];
	return ngEl("div", { class: "md-toolbar", dataset: { noteId: noteRef } },
		actions.map(function (entry) {
			if (!entry) return ngEl("span", { class: "tb-sep" });
			return ngEl("button", { type: "button", text: entry[1], title: entry[2],
				onclick: function () { mdAction(entry[0], area); } });
		}));
}

/* ---- signing out ----
 *
 * One implementation for every way out — the sidebar form and the lock
 * screen's escape. Local data goes with the session: the records are sealed,
 * but a shared machine has no business keeping them. Needs no key and no PIN,
 * so it works from behind the lock.
 */

async function ngLogout() {
	try {
		await ngWipeLocal();
		if (typeof ngClearPin === "function") ngClearPin();
		ngForgetDataKey();
		if (window.caches) {
			for (const name of await caches.keys()) {
				if (name.startsWith("ng-shell-") || name.startsWith("ng-assets-")) await caches.delete(name);
			}
		}
	} catch (err) { /* signing out must happen regardless */ }
	const token = await ngCsrfToken();
	await fetch("/logout", {
		method: "POST",
		headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": token },
		body: new URLSearchParams({ csrf_token: token }),
	}).catch(function () {});
	location.href = "/login";
}

/* ---- links and global actions ---- */

function ngWireGlobalActions() {
	document.addEventListener("click", async function (e) {
		const link = e.target.closest("a[data-link]");
		if (link && link.getAttribute("href") && !e.metaKey && !e.ctrlKey && e.button === 0) {
			e.preventDefault();
			document.body.classList.remove("mobile-nav-open");
			ngNavigate(link.getAttribute("href"));
			return;
		}
		const btn = e.target.closest("[data-action]");
		if (!btn) return;
		switch (btn.dataset.action) {
			case "new-note":
				ngNewNotePrompt(btn.dataset.id || "");
				break;
			case "new-dir":
				ngNewDirPrompt(btn.dataset.id || "");
				break;
			case "toggle-sidebar":
				toggleSidebar();
				break;
			case "toggle-mobile-nav":
				// the class the stylesheet's mobile rules are written against
				document.body.classList.toggle("mobile-nav-open");
				break;
		}
	});
	// Signing out is the one unsafe form left in the shell, and the shell is
	// cached, so its token is supplied at the moment of submitting rather than
	// baked in. Local data goes with it: the records are sealed, but a shared
	// machine has no business keeping them.
	document.addEventListener("submit", function (e) {
		const form = e.target.closest("form.logout-form");
		if (!form) return;
		e.preventDefault();
		ngLogout();
	});

	window.addEventListener("popstate", ngRender);
	ngOnData(function () {
		ngLoadModel().then(ngRender);
		// a pin record can arrive on any pull, not only the first
		if (typeof ngRestorePin === "function") ngRestorePin();
	});
}

/* ---- boot ---- */

// ngHydrateShell fills in what the server would have stamped into the page.
//
// On the web the shell is rendered per request, so it arrives already knowing
// who is signed in and whether this server has the Python runtime and the
// typesetter. The installed app has no such luxury: its shell was rendered once
// at build time, for nobody in particular. Those same three facts are what
// /account answers, so they are fetched instead — and kept, because the answer
// has to survive being offline, which is most of why the app exists.
async function ngHydrateShell() {
	const body = document.body;
	if (!body.dataset.native || body.dataset.user) return;
	let info = null;
	try {
		const resp = await fetch("/account", { headers: { "X-Requested-With": "neuroscribe" } });
		if (resp.ok) {
			info = await resp.json();
			localStorage.setItem("ng-account", JSON.stringify(info));
		}
	} catch (err) { /* offline: the last answer is a good answer */ }
	if (!info) {
		try {
			info = JSON.parse(localStorage.getItem("ng-account") || "null");
		} catch (err) { /* nothing kept: the app will ask for a sign-in */ }
	}
	if (!info || !info.username) return;
	body.dataset.user = info.username;
	if (info.runner_ok) body.dataset.runner = "1";
	if (info.pdf_ok) body.dataset.typst = "1";
}

async function ngBootApp() {
	if (!document.body || !document.body.dataset.app) return;
	await ngHydrateShell();
	await ngLoadStrings();
	ngWireGlobalActions();
	ngWireSyncStatus();
	if (ngLocked()) {
		// No key in this tab. With a PIN on this device the lock screen takes
		// over; without one the only way back to readable notes is the
		// password, so go where it is asked for. Doing neither would leave a
		// signed-in tab blank forever — sessionStorage is per tab.
		if (typeof ngPinFor === "function" && ngPinFor(document.body.dataset.user || "")) {
			return; // lock.js has the screen; it will call back
		}
		location.href = "/login";
		return;
	}
	await ngLoadModel();
	await ngRender();
	ngStartSync();
	// Defaults are seeded only once the server has confirmed the account is
	// empty. A device that cannot reach it waits rather than guessing.
	await ngSync();
	if (await ngMeta("pulled-once", false)) {
		await ngSeedDefaults();
		await ngRender();
	}
	// After the first pull, because the record that arms this device's lock may
	// only just have arrived with it — a device signing in fresh, or one whose
	// storage was cleared, learns its own PIN from the account here.
	if (typeof ngRestorePin === "function") await ngRestorePin();
	// Start Python now if this device draws plots, so that opening a note with
	// a figure in it does not begin with a two-second wait. See run.js.
	if (typeof ngPrewarmPlots === "function") ngPrewarmPlots();
	// figures carry no theme of their own; this re-inks them when it changes
	if (typeof ngWatchTheme === "function") ngWatchTheme();
}

document.addEventListener("DOMContentLoaded", ngBootApp);
