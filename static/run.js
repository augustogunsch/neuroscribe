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
