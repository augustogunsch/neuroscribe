"use strict";

/* Snippet execution, three layers down from the note.
 *
 * The page holding a note frames this document with sandbox="allow-scripts",
 * so everything here runs on an opaque origin. This file then puts the actual
 * snippet inside a worker built from a blob URL, which buys two things:
 * a snippet has no DOM at all to reach for, and a runaway loop can be stopped
 * with terminate() rather than hanging the tab.
 *
 * Python is CPython compiled to WebAssembly (Pyodide), served from this
 * server alongside numpy, scipy, sympy and pandas. Nothing a snippet touches
 * — code, output, imported packages — leaves the browser.
 */

const NG_PYODIDE_URL = new URL("/pyodide/", location.href).href;
const NG_LIMIT_MS = { javascript: 10000, python: 60000 };
const NG_MAX_OUTPUT = 200000;
// A plot is a picture, and a picture of ten thousand points is a megabyte of
// SVG. These bound what one snippet can hand back before it stops being a
// figure and starts being a denial of service against its own note.
const NG_MAX_FIGURE = 4000000;
const NG_MAX_FIGURES = 8;

/* ---- the workers ---- */

// Shared by both languages: output is accumulated as text, and whatever the
// snippet evaluates to is appended the way a REPL would show it.
const NG_JS_WORKER = `
self.onmessage = function (e) {
	const id = e.data.id;
	let out = "";
	const fmt = function (v) {
		if (typeof v === "string") return v;
		if (v instanceof Error) return (v.name || "Error") + ": " + v.message;
		try { const s = JSON.stringify(v); return s === undefined ? String(v) : s; }
		catch (err) { return String(v); }
	};
	const write = function () {
		out += Array.prototype.map.call(arguments, fmt).join(" ") + "\\n";
		if (out.length > ${NG_MAX_OUTPUT}) throw new Error("output limit reached");
	};
	self.console = { log: write, info: write, warn: write, error: write, debug: write, trace: write };
	const finish = function (ok, error) { self.postMessage({ id: id, ok: ok, output: out, error: error }); };
	try {
		// Indirect eval: the snippet evaluates in global scope, where it can
		// neither see nor shadow anything in this handler, and where the last
		// expression is the value — the same thing a console would print.
		const value = (0, eval)(e.data.code);
		Promise.resolve(value).then(
			function (v) { if (v !== undefined) out += fmt(v) + "\\n"; finish(true); },
			function (err) { finish(false, fmt(err)); });
	} catch (err) {
		finish(false, fmt(err));
	}
};
`;

/* Collecting figures.
 *
 * Anything matplotlib drew is pulled out as SVG once the snippet has finished,
 * so a plot arrives as a picture rather than as the words "<Figure size ...>".
 *
 * Two details make it work at all. The backend is forced to Agg, because the
 * one matplotlib picks by default under Pyodide draws into a canvas on the
 * page and there is no page here — this is a worker inside a sandboxed frame,
 * and it has no DOM to draw on. And svg.fonttype is "path", so every label
 * becomes an outline rather than a reference to a font by name: the same SVG
 * then has to render identically in an <img>, and inside a PDF, on a machine
 * that has never heard of DejaVu Sans.
 */
const NG_PY_FIGURES = String.raw`
import sys as _ng_sys
_ng_figs = []
_ng_plt = _ng_sys.modules.get("matplotlib.pyplot")
if _ng_plt is not None:
    import io as _ng_io
    _ng_plt.rcParams["svg.fonttype"] = "path"
    for _ng_n in _ng_plt.get_fignums():
        _ng_buf = _ng_io.StringIO()
        # transparent, so the figure sits on the page rather than on a white
        # card cut out of it. The ink is recoloured for the theme once the SVG
        # is on the page; the background cannot be, because "no background" is
        # the absence of a colour rather than one to swap.
        _ng_plt.figure(_ng_n).savefig(_ng_buf, format="svg", bbox_inches="tight",
                                      transparent=True)
        _ng_figs.append(_ng_buf.getvalue())
    _ng_plt.close("all")
_ng_figs
`;

const NG_PY_WORKER = `
importScripts(${JSON.stringify(NG_PYODIDE_URL)} + "pyodide.js");

let ready = null;
function boot() {
	if (!ready) ready = loadPyodide({ indexURL: ${JSON.stringify(NG_PYODIDE_URL)} });
	return ready;
}

self.onmessage = async function (e) {
	const id = e.data.id;
	let out = "";
	let py = null;
	const write = function (text) {
		out += text + "\\n";
		if (out.length > ${NG_MAX_OUTPUT}) throw new Error("output limit reached");
	};
	try {
		self.postMessage({ id: id, status: "boot" });
		py = await boot();
		py.setStdout({ batched: write });
		py.setStderr({ batched: write });
		// Resolve imports against the wheels shipped with this server. A module
		// we do not carry is not fatal here — Python raises a clearer
		// ImportError than the loader would.
		try {
			self.postMessage({ id: id, status: "packages" });
			await py.loadPackagesFromImports(e.data.code);
		} catch (err) { /* fall through to the interpreter */ }
		// Before any import of matplotlib, and harmless when there is none.
		py.runPython("import os; os.environ.setdefault('MPLBACKEND', 'agg')");
		self.postMessage({ id: id, status: "run" });
		const value = await py.runPythonAsync(e.data.code);
		if (value !== undefined && value !== null) out += String(value) + "\\n";
		self.postMessage({ id: id, ok: true, output: out, figures: collect(py) });
	} catch (err) {
		self.postMessage({
			id: id, ok: false, output: out,
			error: String((err && err.message) || err),
			// a snippet that drew three plots and then raised should still show
			// the three plots
			figures: collect(py),
		});
	}
};

// Pulling the figures out is best-effort: a snippet is not going to be called
// a failure because its pictures could not be collected.
function collect(py) {
	if (!py) return [];
	try {
		const proxy = py.runPython(${JSON.stringify(NG_PY_FIGURES)});
		const figs = proxy.toJs();
		proxy.destroy();
		return figs.filter(function (svg) {
			return typeof svg === "string" && svg.length <= ${NG_MAX_FIGURE};
		}).slice(0, ${NG_MAX_FIGURES});
	} catch (err) {
		return [];
	}
}
`;

/* ---- worker lifecycle ----
 *
 * One worker per language, kept alive between runs: Pyodide takes seconds to
 * start and megabytes to load, and paying that once per note is the difference
 * between usable and not. A timeout kills the worker outright, so the next run
 * starts from a clean interpreter.
 */

const ngWorkers = {};

function ngWorkerFor(lang) {
	if (ngWorkers[lang]) return ngWorkers[lang];
	const source = lang === "python" ? NG_PY_WORKER : NG_JS_WORKER;
	const worker = new Worker(URL.createObjectURL(new Blob([source], { type: "text/javascript" })));
	ngWorkers[lang] = worker;
	return worker;
}

function ngDropWorker(lang) {
	if (!ngWorkers[lang]) return;
	ngWorkers[lang].terminate();
	delete ngWorkers[lang];
}

function ngReply(message) {
	parent.postMessage(message, "*"); // opaque origin: the parent checks the frame instead
}

function ngRun(request) {
	const id = request.id;
	const lang = request.lang === "python" ? "python" : "javascript";
	let worker;
	try {
		worker = ngWorkerFor(lang);
	} catch (err) {
		ngReply({ ng: "done", id: id, ok: false, output: "", error: String(err.message || err) });
		return;
	}

	let settled = false;
	const timer = setTimeout(function () {
		if (settled) return;
		settled = true;
		worker.onmessage = null;
		ngDropWorker(lang); // the only way to stop a spinning loop
		ngReply({ ng: "done", id: id, ok: false, output: "", timedOut: true });
	}, NG_LIMIT_MS[lang]);

	worker.onmessage = function (e) {
		if (settled || !e.data || e.data.id !== id) return;
		if (e.data.status) {
			ngReply({ ng: "status", id: id, status: e.data.status });
			return;
		}
		settled = true;
		clearTimeout(timer);
		ngReply({
			ng: "done", id: id, ok: e.data.ok,
			output: e.data.output || "", error: e.data.error,
			figures: e.data.figures || [],
		});
	};
	worker.onerror = function (err) {
		if (settled) return;
		settled = true;
		clearTimeout(timer);
		ngDropWorker(lang);
		ngReply({ ng: "done", id: id, ok: false, output: "", error: err.message || "worker failed" });
	};

	worker.postMessage({ id: id, code: String(request.code || "") });
}

window.addEventListener("message", function (e) {
	if (!e.data || e.data.ng !== "run") return;
	ngRun(e.data);
});

ngReply({ ng: "ready" });
