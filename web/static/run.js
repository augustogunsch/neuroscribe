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

// ngRunSnippet resolves with {ok, output, error, timedOut} once the snippet
// finishes. onStatus reports the Python runtime's loading stages, which are
// slow enough on a first run to be worth showing.
async function ngRunSnippet(lang, code, onStatus) {
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
	if (!text) {
		body.classList.add("run-empty");
		text = ngT("(no output)");
	}
	body.textContent = text;
	out.replaceChildren(head, body);
}
