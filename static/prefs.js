"use strict";

/* What this reader has chosen: language and colour scheme.
 *
 * Kept on the device first and told to the server second, which is the right
 * way round for a preference — it has to apply with no connection, and the
 * server is only asked to remember it so a second device agrees.
 *
 * Language is the reason this file exists at all. Pages are built in the
 * browser, so there is no server-side rendering step for a template to
 * translate in; a whole language arrives once as a table and is kept, including
 * for the next time there is no network.
 */

/* ---- language ----
 *
 * Pages are built in the browser, so there is no server-side rendering step
 * to translate in. A whole language is fetched once as a table and kept —
 * including for the next time there is no network.
 */

async function ngLoadStrings() {
	const lang = ngPref("lang", (navigator.language || "en").startsWith("pt") ? "pt-BR" : "en");
	document.documentElement.lang = lang;
	if (lang === "en") return;
	const cached = localStorage.getItem("ng-strings-" + lang);
	if (cached) ngSetStrings(JSON.parse(cached));
	try {
		const resp = await fetch("/strings/" + encodeURIComponent(lang) + ".json");
		if (!resp.ok) return;
		const table = await resp.json();
		localStorage.setItem("ng-strings-" + lang, JSON.stringify(table));
		ngSetStrings(table);
	} catch (err) {
		// the cached copy, or English, is a perfectly good answer offline
	}
}

function ngPref(key, def) {
	const v = localStorage.getItem("ng-" + key);
	return v === null ? def : v;
}

function ngSetPref(key, value) {
	localStorage.setItem("ng-" + key, value);
}
