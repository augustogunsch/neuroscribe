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
	const write = function (text) {
		out += text + "\\n";
		if (out.length > ${NG_MAX_OUTPUT}) throw new Error("output limit reached");
	};
	try {
		self.postMessage({ id: id, status: "boot" });
		const py = await boot();
		py.setStdout({ batched: write });
		py.setStderr({ batched: write });
		// Resolve imports against the wheels shipped with this server. A module
		// we do not carry is not fatal here — Python raises a clearer
		// ImportError than the loader would.
		try {
			self.postMessage({ id: id, status: "packages" });
			await py.loadPackagesFromImports(e.data.code);
		} catch (err) { /* fall through to the interpreter */ }
		self.postMessage({ id: id, status: "run" });
		const value = await py.runPythonAsync(e.data.code);
		if (value !== undefined && value !== null) out += String(value) + "\\n";
		self.postMessage({ id: id, ok: true, output: out });
	} catch (err) {
		self.postMessage({ id: id, ok: false, output: out, error: String((err && err.message) || err) });
	}
};
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
