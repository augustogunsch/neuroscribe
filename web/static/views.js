"use strict";

/* The pages.
 *
 * Built here rather than on a server, because a server cannot be assumed: the
 * point of an offline-first app is that a reader on a plane gets the same app
 * as one at a desk. Every view reads the in-memory model (model.js), which was
 * built from IndexedDB, which is the source of truth.
 *
 * The markup and style.css share one vocabulary — the class names here are the
 * ones the stylesheet is written against, so a view that invents its own would
 * render unstyled.
 *
 * Nothing here touches the network. Writing calls into model.js, which stores
 * locally and lets sync.js worry about the server later.
 */

function ngEl(tag, attrs, children) {
	const el = document.createElement(tag);
	Object.entries(attrs || {}).forEach(function ([k, v]) {
		if (v === null || v === undefined || v === false) return;
		if (k === "class") el.className = v;
		else if (k === "text") el.textContent = v;
		else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
		else if (k === "dataset") Object.assign(el.dataset, v);
		else el.setAttribute(k, v === true ? "" : v);
	});
	(children || []).forEach(function (child) {
		if (child) el.appendChild(typeof child === "string" ? document.createTextNode(child) : child);
	});
	return el;
}

function ngViewHost() {
	return document.querySelector("[data-view]");
}

function ngSetTitle(title) {
	document.title = title ? title + " · Neuroscribe" : "Neuroscribe";
}

/* ---- the sidebar tree ----
 *
 * A <details>/<summary> tree: folders fold natively, notes are .notelink
 * anchors, and the row actions appear on hover exactly as the stylesheet
 * expects.
 */

const ngClosedDirs = new Set(); // folders default open, as they always did

function ngRenderTree() {
	const host = document.querySelector("[data-tree]");
	if (!host) return;
	host.replaceChildren(ngTreeLevel(""));
}

function ngTreeLevel(parent) {
	const current = ngCurrentNoteRef();
	const list = ngEl("ul", { class: "tree" });

	ngDirChildren(parent).forEach(function (dir) {
		const details = ngEl("details", { open: !ngClosedDirs.has(dir.ref) });
		details.addEventListener("toggle", function () {
			if (details.open) ngClosedDirs.delete(dir.ref);
			else ngClosedDirs.add(dir.ref);
		});
		const summary = ngEl("summary", { dataset: { dir: dir.ref } }, [
			ngEl("span", { class: "dir-label", text: dir.name }),
			ngEl("span", { class: "row-actions" }, [
				ngEl("button", { type: "button", title: ngT("New note here"), text: "＋📄",
					onclick: function (e) { e.preventDefault(); ngNewNotePrompt(dir.ref); } }),
				ngEl("button", { type: "button", title: ngT("New subfolder"), text: "＋📁",
					onclick: function (e) { e.preventDefault(); ngNewDirPrompt(dir.ref); } }),
				ngEl("button", { type: "button", title: ngT("Rename folder"), text: "✎",
					onclick: function (e) { e.preventDefault(); ngRenameDirPrompt(dir); } }),
				ngEl("button", { type: "button", title: ngT("Delete folder"), text: "✕",
					onclick: function (e) { e.preventDefault(); ngDeleteDirPrompt(dir); } }),
			]),
		]);
		details.appendChild(summary);
		details.appendChild(ngTreeLevel(dir.ref));
		list.appendChild(ngEl("li", { class: "dir" }, [details]));
	});

	ngNotesIn(parent).forEach(function (note) {
		list.appendChild(ngEl("li", { class: "notelink" }, [
			ngEl("a", {
				href: "/notes/" + note.ref,
				class: note.ref === current ? "active" : "",
				text: note.title,
				draggable: "true",
				dataset: { link: "1", note: note.ref },
			}),
		]));
	});
	return list;
}

function ngCurrentNoteRef() {
	const m = /^\/notes\/([^/]+)/.exec(location.pathname);
	return m ? m[1] : "";
}

/* ---- index ---- */

/* The plotting instructions, on the one page that serves as this app's manual.
 *
 * Written to be copied: the marker is the whole feature, and a fence someone
 * can lift straight into a note says it better than a paragraph would.
 */

function ngPlotHint() {
	if (!document.body.dataset.runner) {
		// Saying "add plot to a Python block" on a server that cannot run
		// Python is an instruction that ends in a shrug.
		return ngEl("li", { class: "run-meta",
			text: ngT("Plotting needs the Python runtime, which this server does not have: run `make pyodide` to add it.") });
	}
	return ngEl("li", { text: ngT("Add plot to a Python block and it draws itself with matplotlib — charts, function graphs, vector fields and 3D surfaces — in the note and in the exported PDF.") });
}

function ngPlotExample() {
	if (!document.body.dataset.runner) return ngEl("span");
	const sample = [
		"```python plot",
		"import numpy as np, matplotlib.pyplot as plt",
		"",
		"x = np.linspace(-2*np.pi, 2*np.pi, 400)",
		"plt.plot(x, np.sin(x), label=\"sin x\")",
		"plt.legend(); plt.grid(True)",
		"```",
	].join("\n");

	const pre = ngEl("pre", { class: "plot-sample" });
	pre.appendChild(ngEl("code", { text: sample }));

	const copy = ngEl("button", { type: "button", class: "linklike", text: ngT("Copy") });
	copy.addEventListener("click", async function () {
		try {
			await navigator.clipboard.writeText(sample);
			copy.textContent = ngT("Copied");
			setTimeout(function () { copy.textContent = ngT("Copy"); }, 1500);
		} catch (err) {
			ngToast(ngT("Could not copy."));
		}
	});

	return ngEl("details", { class: "plot-help" }, [
		ngEl("summary", { text: ngT("How to draw a plot") }),
		ngEl("p", { class: "page-hint", text: ngT("Any Python fence with the word plot after the language draws itself when the note opens. A plain python fence keeps its Run button and stays put until you press it.") }),
		pre,
		ngEl("p", { class: "plot-help-actions" }, [copy]),
		ngEl("p", { class: "page-hint", text: ngT("The first one in a session takes a few seconds while Python starts. Everything numpy, scipy and sympy can compute is available to it, and nothing leaves this device.") }),
	]);
}

async function ngViewIndex() {
	const host = ngViewHost();
	const notes = [];
	ngModel.notes.forEach(function (n) { if (!n.trashed) notes.push(n); });
	notes.sort(function (a, b) { return (b.updated_at || "").localeCompare(a.updated_at || ""); });

	const hero = ngEl("div", { class: "hero" }, [
		ngEl("h1", { text: "Neuroscribe" }),
		ngEl("p", { class: "tagline", text: ngT("Your definitive knowledge base — for any subject.") }),
		ngEl("ul", { class: "hints" }, [
			ngEl("li", { text: ngT("Create folders and notes from the sidebar.") }),
			ngEl("li", { text: ngT("Notes are split into chapters, each written in Markdown.") }),
			ngEl("li", {}, [
				ngT("Write math with $…$ and $$…$$ — rendered like LaTeX.") + " ",
				ngEl("a", { href: "https://katex.org/docs/supported", target: "_blank",
					rel: "noopener noreferrer", text: ngT("Every supported function and symbol") }),
				".",
			]),
			ngPlotHint(),
			ngEl("li", { text: ngT("Everything is stored on this device first and works offline; the server only ever receives a sealed copy.") }),
		]),
		ngPlotExample(),
	]);

	const recent = notes.length
		? ngEl("ol", { class: "chapter-list" }, notes.slice(0, 15).map(function (n) {
			return ngEl("li", {}, [
				ngEl("a", { class: "chapter-link", href: "/notes/" + n.ref, dataset: { link: "1" } }, [
					ngEl("span", { text: n.title }),
				]),
				ngEl("span", { class: "chapter-list-actions" }, [
					ngEl("span", { class: "run-meta", text: (n.updated_at || "").slice(0, 10) }),
				]),
			]);
		}))
		: ngEl("p", { class: "page-hint", text: ngT("No notes yet. Use + Note to write the first one.") });

	host.replaceChildren(hero, ngEl("h2", { class: "toc-title", text: ngT("Recent") }), recent);
	ngSetTitle("");
}

/* ---- a note: metadata, chapters, images ---- */

async function ngViewNote(ref) {
	const host = ngViewHost();
	const note = ngModel.notes.get(ref);
	if (!note) return ngViewMissing();
	const chapters = ngChaptersOf(ref);

	const header = ngEl("header", { class: "note-header" }, [
		ngEl("h1", { text: note.title }),
		ngEl("div", { class: "note-actions" }, [
			document.body.dataset.typst
				? ngEl("button", { type: "button", class: "btn primary", text: ngT("Export PDF"),
					onclick: function (e) { ngExportPDFBusy(e.target, ref); } })
				: null,
			ngEl("button", { type: "button", class: "btn", text: ngT("Rename"),
				onclick: function () { ngRenameNotePrompt(note); } }),
			ngEl("button", { type: "button", class: "btn", text: ngT("Move"),
				onclick: function () { ngMoveNotePrompt(note); } }),
			ngEl("button", { type: "button", class: "btn danger", text: ngT("Delete"),
				onclick: async function () {
					if (!(await ngConfirm(ngT("Move this note to the trash?"), true))) return;
					await ngTrashNote(ref);
					ngNavigate("/");
				} }),
		]),
	]);

	// add-chapter is an inline form, not a prompt: naming the chapter is the
	// whole input, so a dialog would be a second step for nothing
	const addChapter = ngEl("form", { class: "add-chapter", onsubmit: async function (e) {
		e.preventDefault();
		const input = e.target.querySelector("input");
		const title = input.value.trim();
		if (!title) return;
		input.value = "";
		await ngCreateChapter(ref, title);
		ngRender();
	} }, [
		ngEl("input", { name: "title", placeholder: ngT("New chapter title…"), required: true, maxlength: "200" }),
		ngEl("button", { type: "submit", class: "primary", text: ngT("Add chapter") }),
	]);

	host.replaceChildren(
		header,
		ngMetaCard(note),
		ngEl("h2", { class: "toc-title", text: ngT("Chapters") }),
		ngChapterList(ref, chapters),
		addChapter,
		ngEl("h2", { class: "toc-title", text: ngT("Images") }),
		ngAttachments(note),
	);
	ngSetTitle(note.title);
}

function ngChapterList(noteRef, chapters) {
	if (!chapters.length) {
		return ngEl("p", { class: "chapter-list-empty page-hint", text: ngT("No chapters yet.") });
	}
	return ngEl("ol", { class: "chapter-list" }, chapters.map(function (c, i) {
		return ngEl("li", {}, [
			ngEl("a", { class: "chapter-link", href: "/notes/" + noteRef + "/" + c.ref, dataset: { link: "1" } }, [
				ngEl("span", { class: "chapter-num", text: (i + 1) + "." }),
				" ",
				ngEl("span", { text: c.title }),
			]),
			ngEl("span", { class: "chapter-list-actions" }, [
				ngEl("button", { type: "button", title: ngT("Move up"), text: "↑", disabled: i === 0,
					onclick: async function () { await ngMoveChapter(c.ref, "up"); ngRender(); } }),
				ngEl("button", { type: "button", title: ngT("Move down"), text: "↓", disabled: i === chapters.length - 1,
					onclick: async function () { await ngMoveChapter(c.ref, "down"); ngRender(); } }),
				ngEl("button", { type: "button", class: "danger", title: ngT("Delete"), text: "✕",
					onclick: async function () {
						if (!(await ngConfirm(ngT("Delete this chapter?"), true))) return;
						await ngDeleteChapter(c.ref);
						ngRender();
					} }),
			]),
		]);
	}));
}

function ngMetaCard(note) {
	const type = ngModel.types.get(note.type);
	const children = [
		ngEl("div", { class: "meta-head" }, [
			ngEl("span", { class: "type-badge", text: (type && type.name) || ngT("Note") }),
			ngEl("button", { type: "button", class: "linklike", text: ngT("Edit metadata"),
				onclick: function () { ngEditMeta(note); } }),
		]),
	];
	if (note.description) {
		children.push(ngEl("p", { class: "note-desc", text: note.description }));
	}
	const fields = (type && type.fields) || [];
	const filled = fields.filter(function (f) { return note.meta && note.meta[f.key]; });
	if (filled.length) {
		children.push(ngEl("dl", { class: "meta-fields" }, filled.map(function (f) {
			const value = f.type === "checkbox" ? ngT("yes") : String(note.meta[f.key]);
			return ngEl("div", { class: "meta-field" }, [
				ngEl("dt", { text: f.label }),
				f.type === "url"
					? ngEl("dd", {}, [ngEl("a", { href: ngSafeURL(note.meta[f.key]), target: "_blank",
						rel: "noopener noreferrer nofollow", text: value })])
					: ngEl("dd", { text: value }),
			]);
		})));
	}
	return ngEl("div", { class: "meta-card" }, children);
}

// ngSafeURL admits http(s) links and nothing else — a metadata field is note
// content, and "javascript:" in an href is the oldest trick there is.
function ngSafeURL(value) {
	try {
		const url = new URL(String(value), location.origin);
		if (url.protocol === "http:" || url.protocol === "https:") return url.href;
	} catch (err) { /* not a URL at all */ }
	return "#";
}

/* ---- images ---- */

function ngAttachments(note) {
	const images = ngImagesOf(note.ref);
	const grid = ngEl("div", { class: "attachments" });

	images.forEach(function (img) {
		const el = ngEl("figure", { class: "attachment" }, [
			ngEl("img", { alt: "", loading: "lazy" }),
			ngEl("figcaption", {}, [
				ngEl("span", { class: "attachment-name", text: img.name || "" }),
				ngEl("span", { class: "attachment-actions" }, [
					ngEl("button", { type: "button", title: ngT("Copy Markdown"), text: "⧉",
						onclick: function () {
							navigator.clipboard.writeText("![](/images/" + img.ref + ")").then(function () {
								ngToast(ngT("Markdown copied"));
							}).catch(function () {});
						} }),
					ngEl("button", { type: "button", class: "danger", title: ngT("Delete"), text: "✕",
						onclick: async function () {
							if (!(await ngConfirm(ngT("Delete this image? Notes that reference it will show a broken link."), true))) return;
							await ngDelete(img.ref);
							ngModel.images.delete(img.ref);
							ngRender();
						} }),
				]),
			]),
		]);
		ngPaintImage(el.querySelector("img"), img.ref);
		grid.appendChild(el);
	});

	const file = ngEl("input", { type: "file", accept: "image/png,image/jpeg,image/gif,image/webp", hidden: true,
		onchange: function () { ngUploadImage(file, note.ref); } });
	grid.appendChild(ngEl("div", { class: "attachment upload-tile" }, [
		ngEl("label", { class: "upload-label" }, [
			file,
			ngEl("span", {}, ["＋", ngEl("br"), ngT("Upload image")]),
		]),
	]));
	return grid;
}

/* ---- a chapter: read, then edit ---- */

async function ngViewChapter(noteRef, chapterRef) {
	const host = ngViewHost();
	const note = ngModel.notes.get(noteRef);
	const chapter = ngModel.chapters.get(chapterRef);
	if (!note || !chapter) return ngViewMissing();
	const chapters = ngChaptersOf(noteRef);
	const at = chapters.findIndex(function (c) { return c.ref === chapterRef; });
	const body = await ngOpenBody(chapterRef);

	const crumbs = ngEl("nav", { class: "breadcrumb" }, [
		ngEl("a", { href: "/notes/" + noteRef, text: note.title, dataset: { link: "1", key: "up" } }),
		ngEl("span", { class: "crumb-sep", text: "/" }),
		ngEl("span", { class: "crumb-here", text: ngTF("Chapter %s of %s", String(at + 1), String(chapters.length)) }),
	]);

	const article = ngEl("article", { class: "chapter-view" }, [
		ngEl("div", { class: "chapter-page-head" }, [
			ngEl("h1", {}, [
				ngEl("span", { class: "chapter-num", text: (at + 1) + "." }),
				" ",
				ngEl("span", { text: chapter.title }),
			]),
			ngEl("div", { class: "note-actions" }, [
				ngEl("button", { type: "button", text: ngT("Rename"),
					onclick: function () { ngRenameChapterPrompt(chapter); } }),
				ngEl("button", { type: "button", class: "primary", text: ngT("Edit"),
					onclick: function () { ngEditChapter(noteRef, chapterRef, body); } }),
			]),
		]),
		ngEl("div", { class: "chapter-body md" }),
	]);

	// prev/next as .btn links; empty spans keep the nav's two ends in place
	const nav = ngEl("nav", { class: "chapter-nav" }, [
		at > 0
			? ngEl("a", { class: "btn", href: "/notes/" + noteRef + "/" + chapters[at - 1].ref,
				dataset: { link: "1", key: "prev" }, text: "← " + chapters[at - 1].title })
			: ngEl("span"),
		at < chapters.length - 1
			? ngEl("a", { class: "btn", href: "/notes/" + noteRef + "/" + chapters[at + 1].ref,
				dataset: { link: "1", key: "next" }, text: chapters[at + 1].title + " →" })
			: ngEl("span"),
	]);

	host.replaceChildren(crumbs, article, nav);
	ngRenderMarkdown(article.querySelector(".chapter-body"), body);
	await ngShowImages(article);
	ngSetTitle((at + 1) + ". " + chapter.title);
}

function ngEditChapter(noteRef, chapterRef, body) {
	const host = ngViewHost();
	const chapter = ngModel.chapters.get(chapterRef);
	const area = ngEl("textarea", { name: "content", spellcheck: "false" });
	area.value = body;
	const form = ngEl("form", { onsubmit: async function (e) {
		e.preventDefault();
		await ngUpdate("chapter", chapterRef, {}, area.value);
		ngRender();
	} }, [
		ngMdToolbar(area, noteRef),
		area,
		ngEl("div", { class: "edit-actions" }, [
			ngEl("button", { type: "submit", class: "primary", text: ngT("Save") }),
			ngEl("button", { type: "button", text: ngT("Cancel"), onclick: function () { ngRender(); } }),
		]),
	]);
	host.replaceChildren(ngEl("article", { class: "chapter-view editing" }, [
		ngEl("div", { class: "chapter-page-head" }, [
			ngEl("h1", { text: chapter ? chapter.title : ngT("Editing") }),
		]),
		form,
	]));
	// a draft the PIN lock sealed is waiting for exactly this textarea
	if (typeof ngRestoreDrafts === "function") ngRestoreDrafts();
	area.focus();
}

/* ---- trash ---- */

async function ngViewTrash() {
	const host = ngViewHost();
	const items = ngTrashedNotes();
	host.replaceChildren(
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Trash") })]),
		ngEl("p", { class: "page-hint", text: ngT("Notes stay here until you empty them. Emptying is permanent on every device.") }),
		items.length
			? ngEl("div", { class: "trash-list" }, items.map(function (n) {
				return ngEl("div", { class: "trash-item" }, [
					ngEl("div", {}, [
						ngEl("div", { class: "trash-title", text: n.title }),
						ngEl("div", { class: "trash-meta", text: ngT("Trashed") + " " + (n.trashed || "").slice(0, 10) }),
					]),
					ngEl("span", { class: "chapter-list-actions" }, [
						ngEl("button", { type: "button", text: ngT("Restore"),
							onclick: async function () { await ngRestoreNote(n.ref); ngRender(); } }),
						ngEl("button", { type: "button", class: "danger", text: ngT("Delete forever"),
							onclick: async function () {
								if (!(await ngConfirm(ngT("Delete this note and everything in it?"), true))) return;
								await ngPurgeNote(n.ref);
								ngRender();
							} }),
					]),
				]);
			}))
			: ngEl("p", { class: "page-hint", text: ngT("The trash is empty.") }),
	);
	ngSetTitle(ngT("Trash"));
}

/* ---- missing ---- */

function ngViewMissing() {
	ngViewHost().replaceChildren(
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Not found") })]),
		ngEl("p", { class: "page-hint", text: ngT("This address is not in this browser's copy. If it was written on another device, it will appear once this one syncs.") }),
	);
	ngSetTitle(ngT("Not found"));
}

/* ---- PDF export with the button saying so ---- */

async function ngExportPDFBusy(button, ref) {
	const label = button.textContent;
	button.disabled = true;
	button.classList.add("is-busy");
	button.textContent = ngT("Typesetting…");
	try {
		await ngExportNotePDF(ref);
	} finally {
		button.disabled = false;
		button.classList.remove("is-busy");
		button.textContent = label;
	}
}
