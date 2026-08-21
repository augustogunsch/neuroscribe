"use strict";

/* Running browser scripts under `node --test`.
 *
 * The frontend is plain scripts, not modules: they define globals and the page
 * loads them in order. Nothing here changes that. Each script is read off disk
 * and evaluated in a fresh vm context whose globals are stubs, so a test can
 * say what sessionStorage held or what the server answered without a browser
 * and without a dependency.
 *
 * The context is given the *host's* TextEncoder, Uint8Array and crypto rather
 * than letting the vm mint its own: WebCrypto rejects a typed array from
 * another realm, so a Uint8Array built inside the context has to be the same
 * Uint8Array node's crypto is checking against.
 *
 * Scripts are compiled under their real file URL, which is what makes
 * `node --test --experimental-test-coverage` attribute the lines it runs to
 * web/static/crypto.js rather than to this file.
 */

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const { pathToFileURL } = require("node:url");

const STATIC = path.resolve(__dirname, "..", "static");

/* ---- stubs ---- */

// The half of Storage these scripts use, and nothing more.
function fakeStorage(initial) {
	const data = new Map(Object.entries(initial || {}));
	return {
		getItem: (k) => (data.has(k) ? data.get(k) : null),
		setItem: (k, v) => data.set(k, String(v)),
		removeItem: (k) => data.delete(k),
		_data: data,
	};
}

// An element is whatever the code reads off one: a value, a dataset, a
// disabled flag. Selectors are a plain map, so a test declares exactly what
// `form.querySelector('input[name="username"]')` is meant to find and an
// unlisted selector returns null the way the real one does.
function fakeElement(props) {
	const el = Object.assign(
		{
			value: "",
			disabled: false,
			className: "",
			textContent: "",
			dataset: {},
			children: [],
			selectors: {},
		},
		props || {},
	);
	el.querySelector = (sel) => (sel in el.selectors ? el.selectors[sel] : null);
	el.prepend = (child) => {
		el.children.unshift(child);
		// a prepended box is findable afterwards, which is what lets
		// ngFormError reuse one instead of creating a second
		if (child.className) {
			child.className.split(" ").forEach((c) => {
				if (c) el.selectors["." + c] = child;
			});
		}
	};
	el.addEventListener = (type, fn) => {
		(el.listeners[type] = el.listeners[type] || []).push(fn);
	};
	el.listeners = {};
	// dispatch returns the handler's promise, so a test can await an async
	// submit handler instead of guessing when it finished
	el.dispatch = (type, event) => {
		const ev = Object.assign({ type: type, preventDefault: () => (ev.defaultPrevented = true) }, event);
		ev.defaultPrevented = false;
		const fns = el.listeners[type] || [];
		return Promise.all(fns.map((fn) => fn(ev))).then(() => ev);
	};
	el.submit = () => {
		el.submitted = (el.submitted || 0) + 1;
	};
	el.submitted = 0;
	return el;
}

function fakeDocument(selectors) {
	const doc = fakeElement({ selectors: selectors || {} });
	doc.createElement = () => fakeElement({});
	doc.written = null;
	doc.open = () => {
		doc.opened = true;
	};
	doc.write = (html) => {
		doc.written = html;
	};
	doc.close = () => {
		doc.closed = true;
	};
	return doc;
}

// fetch, as a queue: each call shifts the next scripted answer. A function in
// the queue is called instead, so a test can make one request throw.
function fakeFetch(responses) {
	const queue = responses.slice();
	const calls = [];
	const fn = async (url, opts) => {
		calls.push({ url: url, opts: opts });
		if (!queue.length) throw new Error("unexpected fetch: " + url);
		const next = queue.shift();
		if (typeof next === "function") return next(url, opts);
		return {
			ok: next.ok !== false,
			status: next.status || 200,
			json: async () => next.json,
			text: async () => next.text || "",
		};
	};
	fn.calls = calls;
	return fn;
}

/* ---- loading ---- */

// load evaluates the named scripts, in order, in one fresh context — the same
// order the page loads them in, so e2e.js finds crypto.js's functions exactly
// as it would in a browser.
function load(files, globals) {
	const sandbox = Object.assign(
		{
			// host intrinsics: WebCrypto refuses buffers from another realm
			crypto: globalThis.crypto,
			TextEncoder: globalThis.TextEncoder,
			TextDecoder: globalThis.TextDecoder,
			Uint8Array: globalThis.Uint8Array,
			ArrayBuffer: globalThis.ArrayBuffer,
			URLSearchParams: globalThis.URLSearchParams,
			btoa: globalThis.btoa,
			atob: globalThis.atob,
			console: console,
			sessionStorage: fakeStorage({}),
		},
		globals || {},
	);
	sandbox.globalThis = sandbox;
	const ctx = vm.createContext(sandbox);
	for (const name of files) {
		const file = path.join(STATIC, name);
		const src = fs.readFileSync(file, "utf8");
		new vm.Script(src, { filename: pathToFileURL(file).href }).runInContext(ctx);
	}
	// `const` at the top level of a script is not a property of the global
	// object, so a constant is reachable only by evaluating its name.
	ctx.eval = (expr) => vm.runInContext(expr, ctx);
	return ctx;
}

module.exports = { load, fakeStorage, fakeElement, fakeDocument, fakeFetch, STATIC };
