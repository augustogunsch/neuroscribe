"use strict";

/* The service worker: what makes this an app rather than a website.
 *
 * Its whole job is to answer requests when nothing else can. Three rules:
 *
 *   navigations   always answered with the cached shell. Every address in the
 *                 app returns the same document, so one cached copy serves
 *                 /notes/<ref> for notes this device has never asked the
 *                 server about — including ones written here while offline.
 *   app assets    cache first, revalidated in the background. They change only
 *                 when the build does.
 *   everything    else goes to the network and is not cached: /sync is the
 *                 live conversation with the server, and caching it would be
 *                 caching the thing the cache exists to avoid needing.
 *
 * The runtimes (Pyodide, Typst) are 149 MB and are not touched unless the
 * reader asks for them in settings, which arrives here as a message.
 */

const NG_VERSION = "v1";
const NG_SHELL_CACHE = "ng-shell-" + NG_VERSION;
const NG_ASSET_CACHE = "ng-assets-" + NG_VERSION;
// Deliberately unversioned: a runtime kept offline is up to 116 MB, and a new
// build must not orphan it behind a new cache name. Its contents are addressed
// by version-stamped paths anyway (/pyodide/…, /typst/…), so there is nothing
// to invalidate.
const NG_RUNTIME_CACHE = "ng-runtime";

// The shell and everything it loads. If any of this is missing offline, the
// app does not start, so it is fetched at install rather than opportunistically.
const NG_PRECACHE = [
	"/",
	"/static/style.css",
	"/static/chroma.css",
	"/static/boot.js",
	"/static/strings.js",
	"/static/crypto.js",
	"/static/strength.js",
	"/static/store.js",
	"/static/sync.js",
	"/static/model.js",
	"/static/zip.js",
	"/static/run.js",
	"/static/csrf.js",
	"/static/app.js",
	"/static/editor.js",
	"/static/render.js",
	"/static/views.js",
	"/static/router.js",
	"/static/prefs.js",
	"/static/types.js",
	"/static/settings.js",
	"/static/export.js",
	"/static/typst.js",
	"/static/pin.js",
	"/static/lock.js",
	// Loaded by other scripts rather than by the shell, which is exactly why
	// they were easy to forget: a module worker and a sandboxed frame.
	"/static/typst-worker.js",
	"/static/runner.html",
	"/static/runner.js",
	"/static/logo.svg",
	"/static/vendor/katex.min.css",
	"/static/vendor/katex.min.js",
	"/static/vendor/marked.min.js",
	"/static/vendor/purify.min.js",
	"/manifest.webmanifest",
];

self.addEventListener("install", function (event) {
	event.waitUntil((async function () {
		const cache = await caches.open(NG_ASSET_CACHE);
		// One at a time and forgiving: a single 404 must not leave the app
		// with no offline copy at all.
		await Promise.all(NG_PRECACHE.map(function (url) {
			return cache.add(new Request(url, { cache: "reload" })).catch(function () {});
		}));
		const shell = await caches.open(NG_SHELL_CACHE);
		try {
			const resp = await fetch(new Request("/", { cache: "reload" }), { credentials: "same-origin" });
			if (resp.ok && resp.headers.get("X-NG-Shell")) await shell.put("/", resp);
		} catch (err) { /* first navigation will cache it instead */ }
		self.skipWaiting();
	})());
});

self.addEventListener("activate", function (event) {
	event.waitUntil((async function () {
		const keep = [NG_SHELL_CACHE, NG_ASSET_CACHE, NG_RUNTIME_CACHE];
		for (const name of await caches.keys()) {
			// runtime caches survive an upgrade: re-downloading 149 MB because
			// a stylesheet changed would be indefensible
			if (!keep.includes(name)) await caches.delete(name);
		}
		await self.clients.claim();
	})());
});

/* ---- answering ---- */

function ngIsAppNavigation(request, url) {
	if (request.mode !== "navigate") return false;
	// the pages that exist before there is an account are the server's, and
	// they must never be answered from a cached signed-in shell
	return !["/login", "/register", "/verify", "/logout"].some(function (p) {
		return url.pathname === p || url.pathname.startsWith(p + "/");
	});
}

self.addEventListener("fetch", function (event) {
	const request = event.request;
	const url = new URL(request.url);
	if (request.method !== "GET" || url.origin !== self.location.origin) return;

	// Never cached: the sync conversation, and anything about the account.
	if (url.pathname === "/sync" || url.pathname.startsWith("/sync/") ||
		url.pathname === "/account" || url.pathname === "/healthz") {
		return;
	}

	if (ngIsAppNavigation(request, url)) {
		event.respondWith(ngShellResponse(request));
		return;
	}

	if (url.pathname.startsWith("/pyodide/") || url.pathname.startsWith("/typst/")) {
		event.respondWith(ngRuntimeResponse(request));
		return;
	}

	if (url.pathname.startsWith("/static/") || url.pathname === "/manifest.webmanifest" ||
		url.pathname.startsWith("/strings/")) {
		event.respondWith(ngAssetResponse(request));
	}
});

// How long a navigation waits for the server before the cached shell answers
// instead. Long enough that a working connection is used, short enough that a
// phone with one bar opens the app rather than spinning.
const NG_SHELL_TIMEOUT_MS = 1500;

/* The shell: from the network when there is one, from the cache when there is
 * not.
 *
 * It used to come from the cache first, which is faster and was wrong. "/" is
 * two different documents — the app shell when you are signed in, the landing
 * page when you are not — and only the server knows which. Answering from the
 * cache meant a signed-out visitor got the shell, the shell found no key, and
 * sent them to /login. The landing page became unreachable on any device that
 * had ever cached the shell, and the only way out was clearing site data.
 *
 * So the network decides, and the cache is the fallback rather than the
 * default. The cost is one round trip for a four-kilobyte document, and only
 * on a real navigation — moving between notes inside the app never comes
 * through here. The offline promise is unchanged: no network, or a network too
 * slow to be worth waiting for, and the cached shell answers.
 */
async function ngShellResponse(request) {
	const cache = await caches.open(NG_SHELL_CACHE);
	let cached = await cache.match("/");
	// Only the real app shell is ever cached — the server marks it. Caching
	// the landing page here would poison every navigation with a document the
	// app cannot boot from.
	if (cached && !cached.headers.get("X-NG-Shell")) {
		await cache.delete("/");
		cached = undefined;
	}

	// The address itself is fetched, not "/", because the server answers them
	// differently: signed in, every app address returns the same shell; signed
	// out, they redirect to the sign-in page. redirect "manual" hands that
	// redirect back to the browser to perform — following it here would render
	// the sign-in page under the address the reader asked for, leaving the
	// location bar lying about which page they are looking at.
	const network = fetch(request.url, {
		credentials: "same-origin",
		redirect: "manual",
	}).then(function (resp) {
		if (resp.ok && resp.type === "basic" && resp.headers.get("X-NG-Shell")) {
			cache.put("/", resp.clone());
		}
		return resp;
	});

	if (!cached) {
		try {
			return await network;
		} catch (err) {
			return new Response("<h1>Offline</h1><p>This device has no cached copy of the app yet.</p>",
				{ status: 503, headers: { "Content-Type": "text/html; charset=utf-8" } });
		}
	}

	// There is a cached shell, so a slow network must not hold the app hostage:
	// whichever answers first wins, and the timer counts as an answer.
	const timeout = new Promise(function (resolve) {
		setTimeout(function () { resolve(null); }, NG_SHELL_TIMEOUT_MS);
	});
	try {
		const resp = await Promise.race([network, timeout]);
		if (resp) return resp;
	} catch (err) {
		// offline, or the server refused: the cached shell is the answer
	}
	network.catch(function () {});
	return cached;
}

async function ngAssetResponse(request) {
	const cache = await caches.open(NG_ASSET_CACHE);
	const cached = await cache.match(request);
	const network = fetch(request).then(function (resp) {
		if (resp.ok) cache.put(request, resp.clone());
		return resp;
	});
	if (cached) {
		network.catch(function () {});
		return cached;
	}
	return network.catch(function () {
		return new Response("", { status: 504 });
	});
}

// Runtimes are only ever served from the cache when they were deliberately
// kept; otherwise they come from the network and are not stored.
async function ngRuntimeResponse(request) {
	const cache = await caches.open(NG_RUNTIME_CACHE);
	const cached = await cache.match(request);
	if (cached) return cached;
	return fetch(request);
}

/* ---- keeping a runtime ---- */

const NG_RUNTIME_MANIFEST = {
	pyodide: "/pyodide/pyodide-lock.json",
	typst: null,
};

async function ngRuntimeUrls(id) {
	if (id === "typst") {
		return [
			"/typst/typst_ts_web_compiler_bg.wasm",
			"/typst/typst_ts_web_compiler.mjs",
			"/typst/typst.mjs",
			"/typst/compiler.mjs",
			"/typst/renderer.mjs",
			"/typst/doc.mjs",
			"/typst/dom.mjs",
			"/typst/init.mjs",
			"/typst/wasm.mjs",
			"/typst/utils.mjs",
			"/typst/main.mjs",
			"/typst/internal.types.mjs",
			"/typst/options.init.mjs",
			"/typst/options.render.mjs",
			"/typst/fonts/NewCM10-Regular.otf",
			"/typst/fonts/NewCM10-Bold.otf",
			"/typst/fonts/NewCM10-Italic.otf",
			"/typst/fonts/NewCM10-BoldItalic.otf",
			"/typst/fonts/NewCMMath-Regular.otf",
			"/typst/fonts/DejaVuSansMono.ttf",
			"/typst/fonts/DejaVuSansMono-Bold.ttf",
			"/typst/packages/mitex/lib.typ",
			"/typst/packages/mitex/mitex.typ",
			"/typst/packages/mitex/mitex.wasm",
			"/typst/packages/mitex/specs/mod.typ",
			"/typst/packages/mitex/specs/prelude.typ",
			"/typst/packages/mitex/specs/latex/standard.typ",
		];
	}
	// Pyodide names its own files, so the list comes from the lock file rather
	// than from a copy here that would rot the moment a version changed.
	const base = ["/pyodide/pyodide.js", "/pyodide/pyodide.asm.js", "/pyodide/pyodide.asm.wasm",
		"/pyodide/python_stdlib.zip", "/pyodide/pyodide-lock.json"];
	try {
		const lock = await (await fetch(NG_RUNTIME_MANIFEST.pyodide)).json();
		Object.values(lock.packages || {}).forEach(function (pkg) {
			base.push("/pyodide/" + pkg.file_name);
		});
	} catch (err) { /* the core alone is still better than nothing */ }
	return base;
}

async function ngCacheRuntimeFiles(id) {
	const cache = await caches.open(NG_RUNTIME_CACHE);
	const urls = await ngRuntimeUrls(id);
	let stored = 0;
	for (const url of urls) {
		try {
			await cache.add(new Request(url, { cache: "reload" }));
			stored++;
		} catch (err) { /* a missing package is not worth failing the lot */ }
	}
	return { cached: stored > 0, files: stored, of: urls.length };
}

async function ngDropRuntime(id) {
	const cache = await caches.open(NG_RUNTIME_CACHE);
	for (const request of await cache.keys()) {
		if (new URL(request.url).pathname.startsWith("/" + id + "/")) await cache.delete(request);
	}
	return { cached: false };
}

async function ngRuntimeStatus(id) {
	const cache = await caches.open(NG_RUNTIME_CACHE);
	const probe = id === "typst" ? "/typst/typst_ts_web_compiler_bg.wasm" : "/pyodide/pyodide.asm.wasm";
	return { cached: !!(await cache.match(probe)) };
}

self.addEventListener("message", function (event) {
	const data = event.data || {};
	const reply = function (answer) {
		if (event.ports && event.ports[0]) event.ports[0].postMessage(answer);
	};
	switch (data.ng) {
		case "runtime-status":
			event.waitUntil(ngRuntimeStatus(data.id).then(reply));
			break;
		case "cache-runtime":
			event.waitUntil(ngCacheRuntimeFiles(data.id).then(reply));
			break;
		case "drop-runtime":
			event.waitUntil(ngDropRuntime(data.id).then(reply));
			break;
		case "skip-waiting":
			self.skipWaiting();
			break;
	}
});
