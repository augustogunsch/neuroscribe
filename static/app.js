"use strict";

/* CSRF: the server double-submits a token via cookie + this meta tag. The only
   unsafe requests left are the sync ones, which echo it back as a header. */
function csrfToken() {
	// Read from the cookie rather than the page: the page is one cached shell
	// serving every address, and a token baked into it goes stale.
	var m = /(?:^|;\s*)ng_csrf=([^;]+)/.exec(document.cookie);
	return m ? m[1] : "";
}

/* ---- enhance rendered markdown: KaTeX math + run buttons ---- */

/* Equation numbering & references. Display math may carry \label{key}
   (auto-numbered) and/or \tag{x} (explicit tag, wins over the number);
   \eqref{key} / \ref{key} anywhere in math becomes a link to the equation.
   Scope is the enhanced fragment: one chapter page numbers 1, 2, 3… */

function texTagText(tag) {
	return tag.replace(/[\\{}]/g, "");
}

function collectEqLabels(mathEls) {
	var labels = {};
	var num = 0;
	mathEls.forEach(function (el) {
		if (el.dataset.rendered || !el.classList.contains("display")) return;
		var tex = el.textContent;
		var labelM = tex.match(/\\label\{([^}]+)\}/);
		var tagM = tex.match(/\\tag\{([^}]+)\}/);
		if (!labelM && !tagM) return;
		var tag = tagM ? tagM[1] : String(++num);
		if (!tagM) el.dataset.eqTag = tag;
		if (labelM) {
			var id = "eq-" + labelM[1].replace(/[^a-zA-Z0-9_-]/g, "-");
			el.id = id;
			labels[labelM[1]] = { tag: tag, id: id };
		}
	});
	return labels;
}

function enhance(root) {
	if (!(root instanceof Element) && root !== document) return;
	var mathEls = root.querySelectorAll(".math");
	var eqLabels = collectEqLabels(mathEls);
	mathEls.forEach(function (el) {
		if (el.dataset.rendered) return;
		el.dataset.rendered = "1";
		var tex = el.textContent;
		// strip labels, apply auto-number tags, resolve references
		tex = tex.replace(/\\label\{[^}]+\}/g, "");
		if (el.dataset.eqTag) tex += "\\tag{" + texTagText(el.dataset.eqTag) + "}";
		tex = tex.replace(/\\(?:eqref|ref)\{([^}]+)\}/g, function (_, key) {
			var target = eqLabels[key];
			if (!target) return "\\text{(?)}";
			return "\\href{#" + target.id + "}{\\text{(" + texTagText(target.tag) + ")}}";
		});
		if (typeof katex !== "undefined") {
			try {
				katex.render(tex, el, {
					displayMode: el.classList.contains("display"),
					throwOnError: false,
					trust: function (ctx) {
						return ctx.command === "\\href" && ctx.url.charAt(0) === "#";
					},
				});
			} catch (e) {
				el.textContent = tex;
			}
		}
	});
	root.querySelectorAll(".codeblock[data-lang]").forEach(function (block) {
		var lang = runnableLang(block.dataset.lang);
		if (!lang || block.querySelector(".run-btn")) return;
		var head = block.querySelector(".codehead");
		if (!head) return;
		var btn = document.createElement("button");
		btn.type = "button";
		btn.className = "run-btn";
		btn.textContent = "▶ " + ngT("Run");
		btn.addEventListener("click", function () { runSnippet(lang, block, btn); });
		head.appendChild(btn);
	});
}

/* ---- running snippets ----
 *
 * Both languages execute in the browser, in a sandboxed frame (see run.js).
 * Nothing here is sent anywhere: no snippet, no output, no timing.
 */

var RUNNABLE = {
	python: "python", py: "python", python3: "python",
	javascript: "javascript", js: "javascript", node: "javascript",
};

function runnableLang(lang) {
	return RUNNABLE[String(lang || "").toLowerCase()] || "";
}

var RUN_STATUS = {
	boot: "Starting Python…",
	packages: "Loading libraries…",
	run: "Running…",
};

async function runSnippet(lang, block, btn) {
	var pre = block.querySelector("pre");
	if (!pre) return;
	btn.disabled = true;
	var out = block.querySelector(".run-output");
	if (!out) {
		out = document.createElement("div");
		out.className = "run-output";
		block.appendChild(out);
	}
	var head = document.createElement("div");
	head.className = "run-head";
	var status = document.createElement("span");
	status.className = "run-meta";
	status.textContent = ngT("Running…");
	head.appendChild(status);
	out.replaceChildren(head);

	var started = performance.now();
	var res;
	try {
		res = await ngRunSnippet(lang, pre.textContent, function (stage) {
			status.textContent = ngT(RUN_STATUS[stage] || "Running…");
		});
	} catch (e) {
		res = { ok: false, output: "", error: String((e && e.message) || e) };
	}
	renderRunResult(out, res, performance.now() - started);
	btn.disabled = false;
}

function renderRunResult(out, res, elapsedMs) {
	var head = document.createElement("div");
	head.className = "run-head";
	var label = document.createElement("span");
	if (res.timedOut) {
		label.className = "run-bad";
		label.textContent = ngT("timed out");
	} else if (res.ok) {
		label.className = "run-ok";
		label.textContent = "exit 0";
	} else {
		label.className = "run-bad";
		label.textContent = ngT("error");
	}
	head.appendChild(label);
	var meta = document.createElement("span");
	meta.className = "run-meta";
	meta.textContent = (elapsedMs / 1000).toFixed(1) + "s";
	head.appendChild(meta);

	var body = document.createElement("pre");
	body.className = "run-out";
	var text = res.output || "";
	if (res.error) text += (text && !text.endsWith("\n") ? "\n" : "") + res.error;
	if (!text) {
		body.classList.add("run-empty");
		text = ngT("(no output)");
	}
	body.textContent = text;
	out.replaceChildren(head, body);
}

/* ---- markdown editor: toolbar + shortcuts + image upload ---- */

function mdTextarea(el) {
	var form = el.closest("form");
	return form ? form.querySelector('textarea[name="content"]') : null;
}

function mdWrap(ta, before, after, placeholder) {
	var start = ta.selectionStart, end = ta.selectionEnd;
	var sel = ta.value.slice(start, end) || placeholder;
	ta.setRangeText(before + sel + after, start, end, "end");
	ta.selectionStart = start + before.length;
	ta.selectionEnd = start + before.length + sel.length;
	ta.focus();
	ta.dispatchEvent(new Event("input", { bubbles: true }));
}

function mdLinePrefix(ta, prefix) {
	var start = ta.selectionStart;
	var lineStart = ta.value.lastIndexOf("\n", start - 1) + 1;
	ta.setRangeText(prefix, lineStart, lineStart, "end");
	ta.selectionStart = ta.selectionEnd = start + prefix.length;
	ta.focus();
}

function mdInsertBlock(ta, text) {
	var start = ta.selectionStart;
	var needsNL = start > 0 && ta.value[start - 1] !== "\n" ? "\n" : "";
	ta.setRangeText(needsNL + text, start, ta.selectionEnd, "end");
	ta.focus();
}

function mdAction(action, toolbar) {
	var ta = mdTextarea(toolbar);
	if (!ta) return;
	switch (action) {
		case "bold": mdWrap(ta, "**", "**", "bold"); break;
		case "italic": mdWrap(ta, "*", "*", "italic"); break;
		case "strike": mdWrap(ta, "~~", "~~", "text"); break;
		case "code": mdWrap(ta, "`", "`", "code"); break;
		case "math": mdWrap(ta, "$", "$", "x^2"); break;
		case "link": mdWrap(ta, "[", "](https://)", "text"); break;
		// ### rather than ##: the chapter title is the page's h1 and rendered
		// Markdown starts one level below it, so an inserted heading should
		// sit under the title, not compete with it
		case "heading": mdLinePrefix(ta, "### "); break;
		case "list": mdLinePrefix(ta, "- "); break;
		case "quote": mdLinePrefix(ta, "> "); break;
		case "codeblock": mdInsertBlock(ta, "```python\n\n```\n"); break;
		case "image": toolbar.querySelector(".img-input").click(); break;
	}
}

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
