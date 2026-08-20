"use strict";

/* The CSRF token, which is a cookie rather than something printed into the
 * page — one cached shell serves every address, so a token baked into it would
 * be a stale token. Used by everything that writes: sync, settings, the PIN
 * routes, signing out.
 */

/* CSRF: the server double-submits a token via cookie + this meta tag. The only
   unsafe requests left are the sync ones, which echo it back as a header. */
function csrfToken() {
	// Read from the cookie rather than the page: the page is one cached shell
	// serving every address, and a token baked into it goes stale.
	var m = /(?:^|;\s*)ng_csrf=([^;]+)/.exec(document.cookie);
	return m ? m[1] : "";
}

// ngCsrfToken is csrfToken for callers that can wait, and the difference
// matters on a phone. The cookie is minted by any response from the server, so
// a browser that has one has been talking to it. A shell that came from the
// cache has not: an app opened from the home screen can be signed in, hold
// every note it needs, and have no token at all — and sending an empty one is
// a guaranteed 403 that reads to the reader as "reload the page". One cheap
// GET is the whole fix; the middleware sets the cookie on the way out.
let ngCsrfPending = null;

async function ngCsrfToken() {
	const have = csrfToken();
	if (have) return have;
	if (!ngCsrfPending) {
		ngCsrfPending = fetch("/healthz", {
			headers: { "X-Requested-With": "neuroscribe" },
			cache: "no-store",
		}).catch(function () { /* offline: nothing to send anyway */ })
			.then(function () { ngCsrfPending = null; return csrfToken(); });
	}
	return ngCsrfPending;
}
