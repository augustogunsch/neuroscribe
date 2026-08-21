"use strict";

/* The frame around the app: the sidebar, dragging notes between folders, and
 * the keyboard shortcuts.
 *
 * Everything here is about the window rather than about a note. The pages
 * themselves are in views.js and router.js; rendering is render.js; what makes
 * a snippet run is run.js.
 */

/* ---- sidebar: resize + collapse (persisted in localStorage) ---- */

/* boot.js has already applied the saved state before first paint; this only
   wires the drag interaction and persistence. */
function initSidebar() {
	var resizer = document.getElementById("sidebar-resizer");
	if (!resizer) return;
	var root = document.documentElement;

	resizer.addEventListener("pointerdown", function (e) {
		if (root.getAttribute("data-sidebar") === "collapsed") return;
		e.preventDefault();
		resizer.setPointerCapture(e.pointerId);
		document.body.classList.add("resizing");
		var width = 0;
		function move(ev) {
			width = Math.min(480, Math.max(180, Math.round(ev.clientX)));
			root.style.setProperty("--sidebar-w", width + "px");
		}
		function up() {
			resizer.removeEventListener("pointermove", move);
			resizer.removeEventListener("pointerup", up);
			document.body.classList.remove("resizing");
			if (width) localStorage.setItem("ng-sidebar-w", String(width));
		}
		resizer.addEventListener("pointermove", move);
		resizer.addEventListener("pointerup", up);
	});
}

function toggleSidebar() {
	var root = document.documentElement;
	var collapsed = root.getAttribute("data-sidebar") !== "collapsed";
	if (collapsed) {
		root.setAttribute("data-sidebar", "collapsed");
	} else {
		root.removeAttribute("data-sidebar");
	}
	localStorage.setItem("ng-sidebar-collapsed", collapsed ? "1" : "0");
}

/* ---- drag notes between folders ----
 *
 * The move itself is a one-field update to the local model; the tree redraws
 * from it, and sync carries the sealed record later. Folders light up via the
 * .drag-over class the stylesheet already knows.
 */

function dropTarget(e) {
	var summary = e.target.closest && e.target.closest(".tree summary");
	if (summary) return summary;
	return e.target.closest && e.target.closest(".treewrap");
}

function clearDropHighlight() {
	document.querySelectorAll(".drag-over").forEach(function (el) {
		el.classList.remove("drag-over");
	});
}

document.addEventListener("dragstart", function (e) {
	var link = e.target.closest && e.target.closest(".notelink a[data-note]");
	if (!link) return;
	e.dataTransfer.setData("text/x-ng-note", link.dataset.note);
	e.dataTransfer.effectAllowed = "move";
});

document.addEventListener("dragover", function (e) {
	if (!e.dataTransfer || Array.prototype.indexOf.call(e.dataTransfer.types, "text/x-ng-note") === -1) return;
	var target = dropTarget(e);
	if (!target) return;
	e.preventDefault();
	e.dataTransfer.dropEffect = "move";
	clearDropHighlight();
	target.classList.add("drag-over");
});

document.addEventListener("dragend", clearDropHighlight);
document.addEventListener("dragleave", function (e) {
	if (!e.relatedTarget || !e.relatedTarget.closest || !e.relatedTarget.closest(".treewrap")) {
		clearDropHighlight();
	}
});

document.addEventListener("drop", async function (e) {
	if (!e.dataTransfer) return;
	var ref = e.dataTransfer.getData("text/x-ng-note");
	if (!ref) return;
	var target = dropTarget(e);
	if (!target) return;
	e.preventDefault();
	clearDropHighlight();
	// a summary is a folder; anywhere else in the tree is the top level
	var dir = target.matches("summary") ? (target.dataset.dir || "") : "";
	var note = ngModel.notes.get(ref);
	if (!note || (note.parent || "") === dir) return;
	await ngUpdate("note", ref, { parent: dir });
	ngRender();
});

/* ---- sync status ----
 *
 * A visible answer to "is my writing safe yet", which matters much more in an
 * app that deliberately keeps working when the answer is "not on the server".
 */

function ngWireSyncStatus() {
	const host = document.querySelector("[data-sync-status]");
	if (!host) return;
	const paint = function (state) {
		host.replaceChildren();
		let text, cls;
		if (!state.online) {
			text = ngT("Offline");
			cls = "run-bad";
		} else if (state.syncing) {
			text = ngT("Syncing…");
			cls = "run-meta";
		} else if (state.pending) {
			text = ngTF("%s waiting to sync", String(state.pending));
			cls = "run-meta";
		} else if (state.error) {
			// online, nothing queued, and the last round still failed: the
			// server is unreachable or unhappy, and saying "Synced" would lie
			text = ngT("Cannot reach the server");
			cls = "run-bad";
		} else {
			text = ngT("Synced");
			cls = "run-ok";
		}
		host.appendChild(ngEl("span", { class: cls, text: text, title: state.error || "" }));
		if (state.pending && state.online && !state.syncing) {
			host.appendChild(ngEl("button", { type: "button", class: "linklike", text: ngT("Sync now"),
				onclick: function () { ngSync(); } }));
		}
	};
	ngOnSyncChange(paint);
	ngSyncState().then(paint);
}

/* ---- keyboard shortcuts ---- */

function inFormField(el) {
	return el.closest("input, textarea, select, [contenteditable]");
}

document.addEventListener("keydown", function (e) {
	// editor shortcuts
	if (e.target.matches('textarea[name="content"]') && (e.metaKey || e.ctrlKey) && !e.altKey) {
		var toolbar = e.target.closest("form").querySelector(".md-toolbar");
		var map = { b: "bold", i: "italic", k: "link" };
		var action = map[e.key.toLowerCase()];
		if (action && toolbar) {
			e.preventDefault();
			mdAction(action, toolbar);
		}
		return;
	}
	// navigation: arrows between chapters, u for the parent note
	if (e.metaKey || e.ctrlKey || e.altKey || e.shiftKey || inFormField(e.target)) return;
	var key = { ArrowLeft: "prev", ArrowRight: "next", u: "up" }[e.key];
	if (!key) return;
	var link = document.querySelector('[data-key="' + key + '"]');
	if (link) {
		e.preventDefault();
		// through the router: a full page load would throw away the model
		ngNavigate(link.getAttribute("href"));
	}
});

/* ---- init ---- */

document.addEventListener("DOMContentLoaded", function () {
	initSidebar();
});
