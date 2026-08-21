"use strict";

/* The page side of snippet execution.
 *
 * All this does is own one hidden, sandboxed iframe and speak to it. The
 * interesting part — the isolation, the workers, the Python runtime — lives in
 * runner.js, on the other side of an origin boundary this page cannot cross
 * and which cannot reach back into this page.
 */

let ngRunnerFrame = null;
let ngRunnerReady = null;
const ngRunPending = new Map();
let ngRunSeq = 0;

function ngRunnerWindow() {
	if (ngRunnerFrame) return ngRunnerReady;
	const frame = document.createElement("iframe");
	// allow-scripts and nothing else: with no allow-same-origin the frame gets
	// an opaque origin, so it has no path to cookies, storage or this DOM.
	frame.setAttribute("sandbox", "allow-scripts");
	frame.src = "/static/runner.html";
	frame.hidden = true;
	frame.style.display = "none";
	ngRunnerFrame = frame;
	ngRunnerReady = new Promise(function (resolve) {
		const onReady = function (e) {
			if (e.source !== frame.contentWindow || !e.data || e.data.ng !== "ready") return;
			window.removeEventListener("message", onReady);
			resolve(frame);
		};
		window.addEventListener("message", onReady);
	});
	document.body.appendChild(frame);
	return ngRunnerReady;
}

window.addEventListener("message", function (e) {
	// The frame is sandboxed, so its messages carry origin "null"; identity
	// comes from the source window instead, which cannot be forged.
	if (!ngRunnerFrame || e.source !== ngRunnerFrame.contentWindow) return;
	const data = e.data;
	if (!data || !ngRunPending.has(data.id)) return;
	const entry = ngRunPending.get(data.id);
	if (data.ng === "status") {
		if (entry.onStatus) entry.onStatus(data.status);
		return;
	}
	if (data.ng !== "done") return;
	ngRunPending.delete(data.id);
	entry.resolve(data);
});

/* One run at a time per language.
 *
 * There is one worker per language on the other side, and it is driven through
 * a single onmessage slot: start a second run before the first has answered
 * and the first is orphaned — its reply is discarded as belonging to another
 * id, its promise never settles, and sixty seconds later its timeout kills the
 * worker out from under whatever is running by then.
 *
 * That was reachable by clicking two Run buttons quickly. It became certain
 * once plots started drawing themselves, because a chapter that renders twice
 * asks for the same figure twice, at once. Queuing is the whole fix: the
 * snippets are independent, and nobody is waiting on two at the same moment.
 */
const ngRunQueue = {};

function ngRunSnippet(lang, code, onStatus) {
	const previous = ngRunQueue[lang] || Promise.resolve();
	const mine = previous
		.catch(function () { /* a failed run must not block the queue */ })
		.then(function () { return ngRunNow(lang, code, onStatus); });
	ngRunQueue[lang] = mine;
	return mine;
}

// ngRunNow resolves with {ok, output, error, timedOut, figures} once the
// snippet finishes. onStatus reports the Python runtime's loading stages,
// which are slow enough on a first run to be worth showing.
async function ngRunNow(lang, code, onStatus) {
	const frame = await ngRunnerWindow();
	const id = ++ngRunSeq;
	return new Promise(function (resolve) {
		ngRunPending.set(id, { resolve: resolve, onStatus: onStatus });
		frame.contentWindow.postMessage({ ng: "run", id: id, lang: lang, code: code }, "*");
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
	var figures = ngFigureElements(res.figures);
	if (!text) {
		body.classList.add("run-empty");
		// a snippet whose whole purpose was the picture has not been silent
		text = figures.length ? ngT("(figure only)") : ngT("(no output)");
	}
	body.textContent = text;
	out.replaceChildren.apply(out, [head, body].concat(figures));
}

/* ---- figures ----
 *
 * A plot arrives as SVG text and goes onto the page as an <img>, never as
 * markup. That is not a detail: inline <svg> is exactly the mutation-XSS
 * surface the sanitizer refuses to allow (see render.js), and this SVG was
 * produced by code the note itself carries. Loaded through an <img>, an SVG
 * cannot run script, reach this DOM or see anything — the same reason images
 * in a note are shown this way.
 */

function ngFigureURL(svg) {
	return URL.createObjectURL(new Blob([svg], { type: "image/svg+xml" }));
}

/* ---- plots ----
 *
 * A fence marked ```python plot draws itself when the note opens, and the
 * result is remembered for as long as the app is unlocked. Remembered in
 * memory only, and deliberately: a rendered figure is a picture of the note's
 * contents, which is the one thing this app does not leave lying around. The
 * lock reloads the page, so the cache dies with the key rather than needing to
 * be swept up.
 *
 * The key is the code itself. Editing a plot redraws it; reopening the note
 * does not.
 */

const ngPlotCache = new Map();

function ngPlotKey(lang, code) {
	return lang + " " + code;
}

/* ---- warming up ----
 *
 * Drawing a figure costs about fifty milliseconds. Getting *ready* to draw one
 * costs two seconds — seven hundred milliseconds to start the interpreter,
 * then numpy, then matplotlib — and that cost is paid once per page and never
 * again. So the whole problem is not that it is expensive, it is that it is
 * paid at the moment someone opens a note rather than while they were deciding
 * which note to open.
 *
 * Hence: pay it in advance. A device that has drawn a figure before starts the
 * interpreter in the background as the app opens, and by the time a note is on
 * screen the figure is fifty milliseconds away. A device that has never drawn
 * one pays the two seconds once and is remembered afterwards.
 *
 * The flag is this device's own: whether you draw plots is not something the
 * server needs to know, and a device that never draws them should never load an
 * interpreter it will not use.
 */

const NG_PLOT_FLAG = "draws-plots";
let ngWarming = null;

async function ngRememberPlots() {
	try {
		if (!(await ngMeta(NG_PLOT_FLAG, false))) await ngSetMeta(NG_PLOT_FLAG, true);
	} catch (err) { /* a flag that will not save costs one slow draw, once */ }
}

// ngWarmPython starts the interpreter and imports matplotlib, discarding the
// result. Safe to call repeatedly; later calls join the first.
function ngWarmPython() {
	if (ngWarming) return ngWarming;
	ngWarming = ngRunSnippet("python", "import matplotlib.pyplot as plt", function () {})
		.catch(function () { /* a real draw will report whatever is wrong */ });
	return ngWarming;
}

// ngPrewarmPlots runs once at boot, when the browser is idle, so that starting
// Python never competes with drawing the page someone is looking at.
async function ngPrewarmPlots() {
	if (!document.body || !document.body.dataset.runner) return;
	try {
		if (!(await ngMeta(NG_PLOT_FLAG, false))) return;
	} catch (err) {
		return;
	}
	const start = function () { ngWarmPython(); };
	if (typeof requestIdleCallback === "function") requestIdleCallback(start, { timeout: 4000 });
	else setTimeout(start, 1200);
}

// ngPlotFigures returns the SVGs a snippet draws, running it only if this
// session has not already seen exactly this code.
//
// What is cached is the promise, not the result, and that is the point: a
// chapter that renders twice asks for the same figure twice within a
// millisecond of itself, and the second ask should join the first rather than
// queue a second identical run behind it.
function ngPlotFigures(lang, code, onStatus) {
	const key = ngPlotKey(lang, code);
	if (ngPlotCache.has(key)) return ngPlotCache.get(key);

	const drawing = ngRunSnippet(lang, code, onStatus).then(function (res) {
		const figures = (res && res.figures) || [];
		if (res && res.ok && figures.length) return figures;
		// Only a clean run is worth keeping. A failure is usually something
		// the reader is about to fix, and remembering it would hide the fix.
		ngPlotCache.delete(key);
		if (res && res.timedOut) return { error: ngT("it took too long") };
		return { error: (res && (res.error || res.output)) || "" };
	}, function (err) {
		ngPlotCache.delete(key);
		return { error: String((err && err.message) || err) };
	});

	ngPlotCache.set(key, drawing);
	return drawing;
}

/* ngPlotMeta reads the directives a plot fence may carry.
 *
 *   #: label=phase
 *   #: The phase portrait near the origin.
 *
 * They are ordinary Python comments, so the interpreter ignores them and the
 * source stays something you could paste into a file and run. Only lines
 * before the first line of real code count, so a "#:" in the middle of a
 * snippet is a comment and nothing more.
 */
function ngPlotMeta(code) {
	var caption = [];
	var label = "";
	var lines = String(code || "").split("\n");
	for (var i = 0; i < lines.length; i++) {
		var line = lines[i].trim();
		if (!line) continue;
		// "#:" in Python, "//:" in Typst — a comment in whichever language the
		// fence is written in, so the source still runs and still pastes.
		var directive = /^(?:#|\/\/)\s*:\s?(.*)$/.exec(line);
		if (!directive) break;
		var rest = directive[1].trim();
		var m = /^label\s*=\s*([A-Za-z0-9_-]{1,64})$/.exec(rest);
		if (m) {
			label = label || m[1];
			continue;
		}
		caption.push(rest);
	}
	return { caption: caption.join(" ").trim(), label: label };
}

/* ngDrawCetz draws a ```plot fence with the typesetter.
 *
 * The same figure, caption and numbering as a Python plot — the difference is
 * only which engine produced the SVG, and by the time it is on the page there
 * is nothing to tell them apart. No Run button, because a drawing is not a
 * program: the source opens and closes with the same small control.
 */
async function ngDrawCetz(block) {
	if (typeof ngCetzFigure !== "function") return;
	const pre = block.querySelector("pre");
	if (!pre || block.dataset.plotDrawn) return;
	const code = pre.textContent;
	const parts = ngPlotFrame(block, ngPlotMeta(code));

	try {
		const svg = await ngCetzFigures(code);
		parts.stage.replaceChildren.apply(parts.stage, ngFigureElements([svg]));
		ngNumberPlots(parts.figure.closest("[data-view], article, body") || document);
	} catch (err) {
		ngPlotFailed(parts, String((err && err.message) || err));
	}
}

/* Drawn once per source per session. Compiling is fast, but a chapter that
 * re-renders should not recompile what has not changed — and, as with the
 * Python path, what is remembered is the promise, so two renders in the same
 * millisecond join one compilation instead of queueing two. */
const ngCetzCache = new Map();

function ngCetzFigures(code) {
	if (ngCetzCache.has(code)) return ngCetzCache.get(code);
	const drawing = ngCetzFigure(code).catch(function (err) {
		ngCetzCache.delete(code);
		throw err;
	});
	ngCetzCache.set(code, drawing);
	return drawing;
}

/* A caption is prose, so $…$ in one is a formula.
 *
 * It does not arrive as prose, though. The note's math is lifted out before
 * Markdown is parsed, and that pass deliberately skips code — a fence's
 * contents have to reach the engine exactly as written. A caption is a comment
 * inside a fence, so it is skipped along with everything else there, and
 * "$\\vec{v}$" stayed a literal five characters in the figure and in the PDF.
 *
 * So the caption gets its own pass, here and in ngTypstCaption, over the same
 * delimiters. Only inline: a caption is a line, not a display.
 */
const NG_CAPTION_MATH = /\$([^\n$]+?)\$/g;

function ngCaptionInto(el, caption) {
	const src = String(caption == null ? "" : caption);
	let at = 0;
	let m;
	NG_CAPTION_MATH.lastIndex = 0;
	while ((m = NG_CAPTION_MATH.exec(src)) !== null) {
		if (m.index > at) el.appendChild(document.createTextNode(src.slice(at, m.index)));
		const span = document.createElement("span");
		span.className = "math inline";
		if (typeof katex !== "undefined") {
			// A caption is not worth losing to a typo in it: a formula that
			// will not parse is shown as what was written.
			katex.render(m[1], span, { throwOnError: false, displayMode: false });
		} else {
			span.textContent = m[0];
		}
		el.appendChild(span);
		at = m.index + m[0].length;
	}
	if (at < src.length) el.appendChild(document.createTextNode(src.slice(at)));
}

/* ngPlotFrame builds the figure a drawing lives in, whichever engine draws it.
 *
 * The block is not decorated, it is replaced: what a reader wants from a plot
 * is the picture, and a picture wearing the frame of a code listing reads as
 * an attachment to the code rather than as the thing itself. The source is
 * still there, folded away behind a small control, because a figure whose
 * source you cannot see is a figure you cannot check.
 */
function ngPlotFrame(block, meta) {
	block.dataset.plotDrawn = "1";

	const figure = document.createElement("figure");
	figure.className = "plot";
	if (meta.label) figure.dataset.plotLabel = meta.label;

	const stage = document.createElement("div");
	stage.className = "plot-stage";
	const note = document.createElement("span");
	note.className = "run-meta";
	note.textContent = ngT("Drawing…");
	stage.appendChild(note);

	const caption = document.createElement("figcaption");
	caption.className = "plot-caption";
	const number = document.createElement("span");
	number.className = "plot-number";
	const text = document.createElement("span");
	text.className = "plot-text";
	ngCaptionInto(text, meta.caption);
	caption.append(number, text);

	// The source, folded away. Moving the original block in here keeps its
	// highlighting — and its Run button, where it has one.
	const source = document.createElement("div");
	source.className = "plot-source";
	source.hidden = true;

	const toggle = document.createElement("button");
	toggle.type = "button";
	toggle.className = "plot-toggle";
	toggle.title = ngT("Show the code that drew this");
	toggle.setAttribute("aria-expanded", "false");
	toggle.textContent = "</>";
	toggle.addEventListener("click", function () {
		source.hidden = !source.hidden;
		toggle.setAttribute("aria-expanded", source.hidden ? "false" : "true");
		toggle.classList.toggle("is-open", !source.hidden);
		toggle.title = source.hidden
			? ngT("Show the code that drew this") : ngT("Hide the code");
	});
	caption.appendChild(toggle);

	figure.append(stage, caption, source);
	block.parentNode.insertBefore(figure, block);
	source.appendChild(block); // the code block now lives inside the figure

	return { figure: figure, stage: stage, source: source, toggle: toggle, note: note };
}

// The one line of an error worth showing.
//
// Two very different shapes arrive here. A Python traceback saves its point
// for last, under the frames; a panic out of the Typst compiler leads with it
// and then unwinds through wasm. Taking the last line reads the first
// correctly and the second exactly backwards — it shows an address inside a
// .wasm file, which says nothing at all about the drawing that failed.
//
// So drop what is plainly a stack frame and take the last of what is left:
// that is the traceback's verdict, and it is the panic's message once its
// frames are gone.
function ngErrorLine(message) {
	const lines = String(message).split("\n").map((l) => l.trim()).filter(Boolean);
	const said = lines.filter((l) => !/^(at\b|File "|\^+$)/.test(l) && !/wasm-function/.test(l));
	const line = said.length ? said[said.length - 1] : lines[0] || "";
	return line.length > 300 ? line.slice(0, 300) + "…" : line;
}

// Nothing drew, so there is no figure: show the source, since the reason is in
// it, and leave the caption unnumbered rather than number a hole.
function ngPlotFailed(parts, message) {
	parts.figure.classList.add("plot-failed");
	parts.source.hidden = false;
	parts.toggle.hidden = true;
	parts.note.className = "run-meta run-bad";
	parts.note.textContent = message
		? ngT("The plot could not be drawn.") + " " + ngErrorLine(message)
		: ngT("That snippet drew no figure.");
}

// ngDrawPlot runs a marked Python fence and shows what it drew.
async function ngDrawPlot(lang, block, btn) {
	const pre = block.querySelector("pre");
	if (!pre || block.dataset.plotDrawn) return;
	const code = pre.textContent;
	const parts = ngPlotFrame(block, ngPlotMeta(code));

	let figures;
	try {
		figures = await ngPlotFigures(lang, code, function (s) {
			parts.note.textContent = ngT(RUN_STATUS[s] || "Drawing…");
		});
	} catch (err) {
		figures = { error: String((err && err.message) || err) };
	}

	if (Array.isArray(figures) && figures.length) {
		parts.stage.replaceChildren.apply(parts.stage, ngFigureElements(figures));
		ngRememberPlots();
		ngNumberPlots(parts.figure.closest("[data-view], article, body") || document);
		return;
	}
	ngPlotFailed(parts, figures && figures.error);
}

/* ---- numbering, and pointing at a figure ----
 *
 * Figures are numbered in the order they appear, and a labelled one can be
 * referred to from the prose as @label. Numbering happens after each figure
 * lands rather than once at the end, because they arrive one at a time and a
 * caption that says "Figure" for a second and then "Figure 2" is worse than
 * one that counts up as they appear.
 *
 * The web numbers within a chapter and the PDF numbers across the whole note,
 * which is exactly what equations already do here.
 */

function ngNumberPlots(root) {
	if (!root || !root.querySelectorAll) return {};
	var labels = {};
	var n = 0;
	root.querySelectorAll("figure.plot").forEach(function (fig) {
		if (fig.classList.contains("plot-failed")) return;
		n += 1;
		var slot = fig.querySelector(".plot-number");
		if (slot) slot.textContent = ngTF("Figure %s", String(n));
		fig.id = "figure-" + n;
		// First claim wins, matching the PDF — where a label may only be
		// attached once, so the second figure to use a name goes unlabelled.
		// Overwriting here instead would point the note at the last copy and
		// the document at the first.
		var label = fig.dataset.plotLabel;
		if (label && !Object.prototype.hasOwnProperty.call(labels, label)) labels[label] = n;
	});
	ngLinkPlotRefs(root, labels);
	return labels;
}

// ngLinkPlotRefs turns "@label" in the prose into a link to that figure. It
// walks text nodes rather than markup: the note has already been sanitized,
// and rebuilding HTML from it is the one thing that would undo that.
function ngLinkPlotRefs(root, labels) {
	if (!Object.keys(labels).length) return;
	var pattern = new RegExp("@(" + Object.keys(labels).map(function (l) {
		return l.replace(/[.*+?^${}()|[\]\\-]/g, "\\$&");
	}).join("|") + ")\\b", "g");

	var walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
	var hits = [];
	while (walker.nextNode()) {
		var node = walker.currentNode;
		// not inside the figure's own caption, and not inside source code
		if (node.parentNode && node.parentNode.closest(".plot-caption, pre, code")) continue;
		if (pattern.test(node.nodeValue)) hits.push(node);
		pattern.lastIndex = 0;
	}

	hits.forEach(function (node) {
		var frag = document.createDocumentFragment();
		var parts = node.nodeValue.split(pattern);
		parts.forEach(function (part, i) {
			if (i % 2 === 0) {
				if (part) frag.appendChild(document.createTextNode(part));
				return;
			}
			var link = document.createElement("a");
			link.href = "#figure-" + labels[part];
			link.className = "plot-ref";
			link.textContent = ngTF("Figure %s", String(labels[part]));
			frag.appendChild(link);
		});
		node.parentNode.replaceChild(frag, node);
	});
}

function ngFigureElements(figures) {
	if (!figures || !figures.length) return [];
	return figures.map(function (svg) {
		var img = document.createElement("img");
		img.className = "run-figure";
		img.alt = ngT("Figure");
		img.src = ngFigureURL(svg);
		// the object URL is this document's to keep; releasing it on unload is
		// enough, and releasing it earlier would blank an image still on screen
		img.addEventListener("load", function () { img.dataset.ready = "1"; });
		return img;
	});
}
