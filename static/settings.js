"use strict";

/* Settings, note types, and the parts of the interface that talk about the
 * server rather than to it.
 *
 * Everything here works offline except the account panel, which is the one
 * genuinely server-side fact in the app: how much of the plan is used. When
 * there is no connection it shows the last answer and says so.
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

/* ---- sync status ----
 *
 * A visible answer to "is my writing safe yet", which matters much more in an
 * app that deliberately keeps working when the answer is "not on the server".
 */

function ngWireSyncStatus() {
	const host = document.querySelector("[data-sync-status]");
	if (!host) return;
	const paint = function (state) {
		host.replaceChildren();
		let text, cls;
		if (!state.online) {
			text = ngT("Offline");
			cls = "run-bad";
		} else if (state.syncing) {
			text = ngT("Syncing…");
			cls = "run-meta";
		} else if (state.pending) {
			text = ngTF("%s waiting to sync", String(state.pending));
			cls = "run-meta";
		} else if (state.error) {
			// online, nothing queued, and the last round still failed: the
			// server is unreachable or unhappy, and saying "Synced" would lie
			text = ngT("Cannot reach the server");
			cls = "run-bad";
		} else {
			text = ngT("Synced");
			cls = "run-ok";
		}
		host.appendChild(ngEl("span", { class: cls, text: text, title: state.error || "" }));
		if (state.pending && state.online && !state.syncing) {
			host.appendChild(ngEl("button", { type: "button", class: "linklike", text: ngT("Sync now"),
				onclick: function () { ngSync(); } }));
		}
	};
	ngOnSyncChange(paint);
	ngSyncState().then(paint);
}

/* ---- note types ---- */

const NG_FIELD_TYPES = ["text", "number", "date", "url", "checkbox"];

async function ngViewTypes() {
	const host = ngViewHost();
	const types = Array.from(ngModel.types.values()).sort(ngByName);

	const newType = ngEl("form", { class: "add-chapter", onsubmit: async function (e) {
		e.preventDefault();
		const input = e.target.querySelector("input");
		const name = input.value.trim();
		if (!name) return;
		input.value = "";
		await ngCreateType(name, []);
		ngRender();
	} }, [
		ngEl("input", { name: "name", placeholder: ngT("New type name…"), required: true, maxlength: "200" }),
		ngEl("button", { type: "submit", class: "primary", text: ngT("Create type") }),
	]);

	host.replaceChildren(
		ngEl("nav", { class: "breadcrumb" }, [
			ngEl("a", { href: "/settings", text: ngT("Settings"), dataset: { link: "1" } }),
			ngEl("span", { class: "crumb-sep", text: "/" }),
			ngEl("span", { class: "crumb-here", text: ngT("Note types") }),
		]),
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Note types") })]),
		ngEl("p", { class: "page-hint", text: ngT("Each note has a type. A type defines extra metadata fields — every note always has a title and a description.") }),
		newType,
		ngEl("div", {}, types.map(ngTypeCard)),
	);
	ngSetTitle(ngT("Note types"));
}

function ngTypeCard(type) {
	const used = Array.from(ngModel.notes.values())
		.filter(function (n) { return n.type === type.ref; }).length;
	const fields = type.fields || [];

	const head = ngEl("div", { class: "type-card-head" }, [
		ngEl("h2", { text: type.name }),
		ngEl("span", { class: "type-usage", text: ngTF("%s notes", String(used)) }),
		ngEl("button", { type: "button", text: ngT("Rename"), onclick: async function () {
			const name = await ngAsk(ngT("New type name"), type.name);
			if (!name) return;
			await ngUpdate("type", type.ref, { name: name });
			ngRender();
		} }),
		ngEl("button", { type: "button", class: "danger", text: ngT("Delete type"),
			disabled: used > 0,
			title: used > 0 ? ngTF("cannot delete: %s note(s) use this type", String(used)) : "",
			onclick: async function () {
				if (!(await ngConfirm(ngT("Delete this type?"), true))) return;
				await ngDelete(type.ref);
				ngModel.types.delete(type.ref);
				ngRender();
			} }),
	]);

	const table = fields.length
		? ngEl("table", { class: "fields-table" }, [
			ngEl("thead", {}, [ngEl("tr", {}, [
				ngEl("th", { text: ngT("Field") }),
				ngEl("th", { text: ngT("Data type") }),
				ngEl("th"),
			])]),
			ngEl("tbody", {}, fields.map(function (f, i) {
				return ngEl("tr", {}, [
					ngEl("td", { text: f.label }),
					ngEl("td", {}, [ngEl("code", { text: f.type })]),
					ngEl("td", {}, [
						ngEl("button", { type: "button", class: "danger", title: ngT("Remove field"), text: "✕",
							onclick: async function () {
								const next = fields.slice(0, i).concat(fields.slice(i + 1));
								await ngUpdate("type", type.ref, { fields: next });
								ngRender();
							} }),
					]),
				]);
			})),
		])
		: ngEl("p", { class: "type-empty page-hint", text: ngT("No extra fields.") });

	const label = ngEl("input", { placeholder: ngT("New field label…"), maxlength: "100" });
	const kind = ngEl("select", {}, NG_FIELD_TYPES.map(function (t) {
		return ngEl("option", { value: t, text: t });
	}));
	const addField = ngEl("form", { class: "add-field", onsubmit: async function (e) {
		e.preventDefault();
		const text = label.value.trim();
		if (!text) return;
		const key = text.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "field";
		await ngUpdate("type", type.ref, {
			fields: fields.concat([{ key: key, label: text, type: kind.value }]),
		});
		ngRender();
	} }, [
		label, kind,
		ngEl("button", { type: "submit", text: ngT("Add field") }),
	]);

	return ngEl("section", { class: "type-card" }, [head, table, addField]);
}

/* ---- settings ---- */

let ngAccountCache = null;

async function ngFetchAccount() {
	try {
		const resp = await fetch("/account", { headers: { "X-Requested-With": "neuroscribe" } });
		if (!resp.ok) throw new Error("no account");
		ngAccountCache = await resp.json();
		localStorage.setItem("ng-account", JSON.stringify(ngAccountCache));
		return ngAccountCache;
	} catch (err) {
		const cached = localStorage.getItem("ng-account");
		return cached ? Object.assign(JSON.parse(cached), { stale: true }) : null;
	}
}

// ngPasswordCard rotates the password without the server ever seeing one.
// The data key this session already holds is re-wrapped under the new
// password, so nothing is re-encrypted and the PIN (which wraps the same data
// key directly) keeps working.
function ngPasswordCard() {
	const current = ngEl("input", { type: "password", autocomplete: "current-password", required: true });
	const next = ngEl("input", { type: "password", autocomplete: "new-password", required: true,
		minlength: "12", maxlength: "256" });
	const repeat = ngEl("input", { type: "password", autocomplete: "new-password", required: true,
		minlength: "12", maxlength: "256" });
	const meter = ngEl("div", { class: "pw-meter" });
	const error = ngEl("p", { class: "warn slim", hidden: true });
	const save = ngEl("button", { type: "submit", class: "primary", text: ngT("Change password") });
	// context for the estimator: the username is static here, so a stand-in
	// with an input's shape is enough
	ngWireStrengthMeter(next, meter, [{
		value: document.body.dataset.user || "",
		addEventListener: function () {},
	}]);

	const fail = function (message) {
		error.textContent = message;
		error.hidden = false;
	};

	const form = ngEl("form", { class: "settings-form", onsubmit: async function (e) {
		e.preventDefault();
		error.hidden = true;
		if (next.value !== repeat.value) {
			fail(ngT("The two passwords do not match."));
			return;
		}
		const rating = await ngRatePassword(next.value, [document.body.dataset.user || ""]);
		if (!rating.ok) {
			fail((rating.detail ? rating.detail + " " : "") + ngT("Choose a stronger password before continuing."));
			return;
		}
		const storedKey = sessionStorage.getItem(NG_DK_STORAGE);
		if (!storedKey) {
			fail(ngT("Unlock the app first."));
			return;
		}
		save.disabled = true;
		save.textContent = ngT("Changing…");
		try {
			// prove the current password the way login does
			const me = document.body.dataset.user || "";
			const params = await (await fetch("/auth/params?username=" + encodeURIComponent(me))).json();
			const old = await ngDeriveKeys(current.value, params.salt);
			// and wrap the data key this session already holds under the new one
			const salt = ngB64(ngRandom(16));
			const fresh = await ngDeriveKeys(next.value, salt);
			const wrapped = await ngSeal(fresh.encKey, storedKey);
			const resp = await fetch("/auth/password", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": csrfToken() },
				body: new URLSearchParams({
					old_auth_key: old.authKey,
					new_auth_key: fresh.authKey,
					new_salt: salt,
					new_wrapped_key: wrapped,
				}),
			});
			if (!resp.ok) {
				fail(resp.status === 403 ? ngT("The current password is wrong.")
					: (await resp.text()).trim() || ngT("The password could not be changed."));
				return;
			}
			current.value = next.value = repeat.value = "";
			ngToast(ngT("Password changed. Other devices were signed out."));
		} catch (err) {
			fail(ngT("The password could not be changed.") + " " + String((err && err.message) || err));
		} finally {
			save.disabled = false;
			save.textContent = ngT("Change password");
		}
	} }, [
		ngEl("label", { class: "meta-label", text: ngT("Current password") + " " }, [current]),
		ngEl("label", { class: "meta-label", text: ngT("New password") + " " }, [next]),
		meter,
		ngEl("label", { class: "meta-label", text: ngT("Repeat new password") + " " }, [repeat]),
		error,
		ngEl("div", {}, [save]),
	]);

	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Change password") }),
		ngEl("p", { class: "page-hint", text: ngT("Your notes are re-locked under the new password without being re-encrypted: the key that seals them never changes and never leaves this browser. Every other signed-in device is signed out.") }),
		form,
	]);
}

// ngPinCard builds the PIN panel. lock.js finds it by these data attributes
// and wires the behaviour, so the two form a single contract.
function ngPinCard() {
	const idle = ngEl("select", { "data-pin-idle": true }, [
		["1", ngT("1 minute")], ["5", ngT("5 minutes")], ["15", ngT("15 minutes")],
		["30", ngT("30 minutes")], ["60", ngT("1 hour")], ["0", ngT("Never (only on reopening)")],
	].map(function ([v, label]) {
		return ngEl("option", { value: v, text: label, selected: v === "15" });
	}));
	return ngEl("section", { class: "type-card", "data-pin-settings": true, hidden: true }, [
		ngEl("h2", { text: ngT("Device PIN") }),
		ngEl("p", { class: "page-hint", text: ngT("A six-digit PIN unlocks this device instead of your full password. The PIN travels with your notes, sealed with your key, so signing in again brings this device's lock back — the server holds it and cannot read it. The key sealed under the PIN never leaves this device.") }),
		ngEl("dl", { class: "meta-fields" }, [
			ngEl("div", { class: "meta-field" }, [
				ngEl("dt", { text: ngT("Status") }),
				ngEl("dd", { "data-pin-status": true }),
			]),
		]),
		ngEl("form", { class: "settings-form", "data-pin-form": true }, [
			ngEl("label", { class: "meta-label", text: ngT("New PIN") + " " }, [
				ngEl("input", { type: "password", inputmode: "numeric", pattern: "[0-9]*",
					maxlength: "6", autocomplete: "off", "data-pin-new": true }),
			]),
			ngEl("label", { class: "meta-label", text: ngT("Repeat PIN") + " " }, [
				ngEl("input", { type: "password", inputmode: "numeric", pattern: "[0-9]*",
					maxlength: "6", autocomplete: "off", "data-pin-confirm": true }),
			]),
			ngEl("label", { class: "meta-label", text: ngT("Lock after") + " " }, [idle]),
			ngEl("label", { class: "meta-label", text: ngT("Your password, to confirm") + " " }, [
				ngEl("input", { type: "password", autocomplete: "current-password",
					"data-pin-password": true }),
			]),
			ngEl("p", { class: "warn slim", "data-pin-error": true, hidden: true }),
			ngEl("div", { class: "pin-actions" }, [
				ngEl("button", { type: "submit", class: "primary", "data-pin-save": true, text: ngT("Set PIN") }),
				ngEl("button", { type: "button", "data-pin-lock": true, hidden: true, text: ngT("Lock now") }),
				ngEl("button", { type: "button", class: "danger", "data-pin-remove": true, hidden: true, text: ngT("Remove PIN") }),
			]),
		]),
		ngEl("div", { "data-pin-devices": true }),
		ngEl("p", { class: "page-hint pin-note", text: ngT("Six digits is a million combinations: enough to stop someone who picks up your unlocked laptop, not enough to stop someone who copies this browser's storage and guesses at their leisure. Ten wrong tries erase the sealed key from this device, and your password still gets you back in.") }),
	]);
}

async function ngViewSettings() {
	const host = ngViewHost();
	host.replaceChildren(
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Settings") })]),
		ngPrefsCard(),
		ngEl("section", { class: "type-card" }, [
			ngEl("h2", { text: ngT("Note types") }),
			ngEl("p", { class: "page-hint", text: ngT("Define note types and their custom metadata fields.") }),
			ngEl("a", { class: "btn", href: "/types", text: ngT("Manage note types"), dataset: { link: "1" } }),
		]),
		ngPasswordCard(),
		ngPinCard(),
		ngOfflineCard(),
		ngExportCard(),
		ngRepairCard(),
		ngEl("div", { dataset: { accountHost: "1" } }, [
			ngEl("p", { class: "page-hint", text: ngT("Loading…") }),
		]),
		ngDisclosureCard(),
		ngDeleteAccountCard(),
	);
	ngWireLockSettings();
	const account = await ngFetchAccount();
	const slot = host.querySelector("[data-account-host]");
	if (slot) slot.replaceChildren(ngAccountCard(account));
}

function ngPrefsCard() {
	const lang = ngEl("select", {}, [
		ngEl("option", { value: "en", text: "English", selected: ngPref("lang", "en") === "en" }),
		ngEl("option", { value: "pt-BR", text: "Português (Brasil)", selected: ngPref("lang", "en") === "pt-BR" }),
	]);
	const theme = ngEl("select", {}, ["auto", "light", "dark"].map(function (t) {
		return ngEl("option", { value: t, text: ngT(t === "auto" ? "Auto (system)" : t === "light" ? "Light" : "Dark"),
			selected: ngPref("theme", "dark") === t });
	}));
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Preferences") }),
		ngEl("form", { class: "settings-form", onsubmit: function (e) {
			e.preventDefault();
			ngSetPref("lang", lang.value);
			ngSetPref("theme", theme.value);
			document.documentElement.setAttribute("data-theme", theme.value);
			// stored locally first so it applies offline; the server is told
			// only so another device agrees
			fetch("/account/prefs", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": csrfToken() },
				body: new URLSearchParams({ lang: lang.value, theme: theme.value }),
			}).catch(function () { /* it will disagree until next time; harmless */ });
			location.reload();
		} }, [
			ngEl("label", { class: "meta-label", text: ngT("Language") + " " }, [lang]),
			ngEl("label", { class: "meta-label", text: ngT("Color scheme") + " " }, [theme]),
			ngEl("div", {}, [ngEl("button", { type: "submit", class: "primary", text: ngT("Save preferences") })]),
		]),
	]);
}

/* ---- keeping the heavy runtimes offline ----
 *
 * 149 MB between them, so neither is downloaded until it is asked for. The
 * service worker does the caching; this is only the switch.
 */

function ngOfflineCard() {
	const rows = [
		{ id: "pyodide", label: ngT("Python for snippets"), size: "116 MB", available: !!document.body.dataset.runner },
		{ id: "typst", label: ngT("The typesetter, for PDF export"), size: "33 MB", available: !!document.body.dataset.typst },
	];
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Available offline") }),
		ngEl("p", { class: "page-hint", text: ngT("Your notes are always kept on this device. These two are large, so they are only downloaded if you ask.") }),
		ngEl("ul", { class: "runtime-list" }, rows.map(function (row) {
			const status = ngEl("span", { class: "run-meta", text: ngT("checking…") });
			const button = ngEl("button", { type: "button", text: ngT("Keep offline"), disabled: !row.available });
			ngRuntimeCached(row.id).then(function (cached) {
				status.textContent = cached ? ngT("kept on this device")
					: row.available ? ngT("not kept") : ngT("not installed on the server");
				status.className = cached ? "run-ok" : "run-meta";
				button.textContent = cached ? ngT("Remove") : ngT("Keep offline");
				button.onclick = async function () {
					button.disabled = true;
					button.textContent = cached ? ngT("Removing…") : ngT("Downloading…");
					await ngCacheRuntime(row.id, !cached);
					ngRender();
				};
			});
			return ngEl("li", { class: "runtime-row" }, [
				ngEl("div", { class: "runtime-name" }, [
					ngEl("strong", { text: row.label }),
					ngEl("span", { class: "run-meta", text: " · " + row.size }),
					ngEl("div", {}, [status]),
				]),
				button,
			]);
		})),
	]);
}

function ngSWMessage(message) {
	return new Promise(function (resolve) {
		if (!navigator.serviceWorker || !navigator.serviceWorker.controller) return resolve(null);
		const channel = new MessageChannel();
		channel.port1.onmessage = function (e) { resolve(e.data); };
		navigator.serviceWorker.controller.postMessage(message, [channel.port2]);
	});
}

async function ngRuntimeCached(id) {
	const answer = await ngSWMessage({ ng: "runtime-status", id: id });
	return !!(answer && answer.cached);
}

async function ngCacheRuntime(id, want) {
	return ngSWMessage({ ng: want ? "cache-runtime" : "drop-runtime", id: id });
}

/* ---- export ---- */

// A recovery tool, not a routine one: pushes this device's entire copy back
// to the server. Exists for the day a server falls behind a device — old
// stranded edits, a restored server backup — and for peace of mind after one.
function ngRepairCard() {
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Sync repair") }),
		ngEl("p", { class: "page-hint", text: ngT("If another device shows older content than this one, the server fell behind this device's copy. This pushes everything this device holds back to the server.") }),
		ngEl("button", { type: "button", class: "btn", text: ngT("Push this device's copy to the server"),
			onclick: async function (e) {
				const btn = e.target;
				btn.disabled = true;
				const n = await ngMarkAllDirty();
				await ngSync();
				btn.disabled = false;
				ngToast(ngTF("%s records queued and synced.", String(n)));
			} }),
	]);
}

// Deleting the account. Two locks, because this is the one irreversible
// button in the app: the password, and typing the username. Nothing here is
// recoverable afterwards — not by support, not by a backup, not by the
// server, which never had a readable copy in the first place.
function ngDeleteAccountCard() {
	const me = document.body.dataset.user || "";
	const confirmName = ngEl("input", { type: "text", autocomplete: "off", placeholder: me });
	const password = ngEl("input", { type: "password", autocomplete: "current-password" });
	const error = ngEl("p", { class: "warn slim", hidden: true });
	const button = ngEl("button", { type: "submit", class: "danger", text: ngT("Delete my account") });

	const fail = function (message) {
		error.textContent = message;
		error.hidden = false;
	};

	const form = ngEl("form", { class: "settings-form", onsubmit: async function (e) {
		e.preventDefault();
		error.hidden = true;
		if (confirmName.value.trim() !== me) {
			fail(ngTF("Type %s to confirm.", me));
			return;
		}
		if (!password.value) {
			fail(ngT("Enter your password to confirm."));
			return;
		}
		if (!(await ngConfirm(ngT("Delete this account and everything in it? This cannot be undone."), true))) {
			return;
		}
		button.disabled = true;
		button.textContent = ngT("Deleting…");
		try {
			// proven the way signing in proves it: only the derived key travels
			const params = await (await fetch("/auth/params?username=" + encodeURIComponent(me))).json();
			const derived = await ngDeriveKeys(password.value, params.salt);
			const resp = await fetch("/account/delete", {
				method: "POST",
				headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": csrfToken() },
				body: new URLSearchParams({ password_auth: derived.authKey }),
			});
			if (!resp.ok) {
				fail(resp.status === 403 ? ngT("Wrong password.")
					: (await resp.text()).trim() || ngT("The account could not be deleted."));
				return;
			}
			// the server is done; leave nothing on this device either
			await ngWipeLocal();
			if (typeof ngClearPin === "function") ngClearPin();
			ngForgetDataKey();
			localStorage.clear();
			if (window.caches) {
				for (const name of await caches.keys()) await caches.delete(name);
			}
			if (navigator.serviceWorker) {
				for (const reg of await navigator.serviceWorker.getRegistrations()) await reg.unregister();
			}
			location.href = "/";
		} catch (err) {
			fail(String((err && err.message) || err));
		} finally {
			button.disabled = false;
			button.textContent = ngT("Delete my account");
		}
	} }, [
		ngEl("label", { class: "meta-label", text: ngTF("Type %s to confirm", me) + " " }, [confirmName]),
		ngEl("label", { class: "meta-label", text: ngT("Your password") + " " }, [password]),
		error,
		ngEl("div", {}, [button]),
	]);

	return ngEl("section", { class: "type-card danger-zone" }, [
		ngEl("h2", { text: ngT("Delete account") }),
		ngEl("p", { class: "warn slim", text: ngT("This erases the account and every note, chapter, image and note type in it, on the server and on this device. There is no backup and no recovery: the server never had a readable copy, so nobody can restore this for you.") }),
		ngEl("p", { class: "page-hint", text: ngT("Export your notes first if you want to keep them.") }),
		form,
	]);
}

function ngExportCard() {
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Export") }),
		ngEl("p", { class: "page-hint", text: ngT("Download everything as a zip: the Markdown source of every note, plus your images. Built here, from this device's copy, it works offline too.") }),
		ngEl("button", { type: "button", class: "btn primary", text: ngT("Export everything (.zip)"),
			onclick: function (e) { ngExportEverything(e.target); } }),
	]);
}

function ngAccountCard(account) {
	if (!account) {
		return ngEl("section", { class: "type-card" }, [
			ngEl("h2", { text: ngT("Plan and limits") }),
			ngEl("p", { class: "page-hint", text: ngT("Unavailable offline, this is the one thing only the server knows.") }),
		]);
	}
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("Plan and limits") }),
		ngEl("p", { class: "page-hint", text: ngTF("Your account is on the %s plan.", account.plan) }),
		ngEl("dl", { class: "meta-fields" }, [
			ngEl("div", { class: "meta-field" }, [ngEl("dt", { text: ngT("Notes") }),
				ngEl("dd", { text: ngTF("%s of %s", String(account.notes), String(account.max_notes)) })]),
			ngEl("div", { class: "meta-field" }, [ngEl("dt", { text: ngT("Images") }),
				ngEl("dd", { text: ngTF("%s of %s", String(account.images), String(account.max_images)) })]),
			ngEl("div", { class: "meta-field" }, [ngEl("dt", { text: ngT("Largest image") }),
				ngEl("dd", { text: account.image_cap })]),
			ngEl("div", { class: "meta-field" }, [ngEl("dt", { text: ngT("Largest chapter") }),
				ngEl("dd", { text: account.note_cap })]),
		]),
		account.stale ? ngEl("p", { class: "page-hint", text: ngT("Shown from the last time this device was online.") }) : null,
	]);
}

function ngDisclosureCard() {
	const rows = [
		[ngT("Chapter text"), ngT("no, encrypted in your browser"), true],
		[ngT("Note, chapter and folder names"), ngT("no, the addresses of your pages are random"), true],
		[ngT("Descriptions, metadata fields and note types"), ngT("no"), true],
		[ngT("Images you upload"), ngT("no"), true],
		[ngT("Code snippets and their output"), ngT("no, they run in your browser and are never sent"), true],
		[ngT("Your PIN, if you set one"), ngT("no, it never leaves this browser"), true],
		[ngT("During PDF or zip export"), ngT("no, both are built in your browser"), true],
		[ngT("How many notes and folders you have, and their sizes and dates"), ngT("yes, the database has to be organised somehow"), false],
		[ngT("Your email address"), ngT("yes, it is how you sign in and are contacted"), false],
	];
	return ngEl("section", { class: "type-card" }, [
		ngEl("h2", { text: ngT("What this server can read") }),
		ngEl("p", { class: "page-hint", text: ngT("Everything you write is encrypted before it is sent. What remains visible is the shape of the database, not its contents:") }),
		ngEl("table", { class: "fields-table disclosure" }, [
			ngEl("thead", {}, [ngEl("tr", {}, [
				ngEl("th", { text: ngT("Stored") }), ngEl("th", { text: ngT("Readable by the server") })])]),
			ngEl("tbody", {}, rows.map(function ([what, answer, ok]) {
				return ngEl("tr", {}, [ngEl("td", { text: what }),
					ngEl("td", { class: ok ? "run-ok" : "run-bad", text: answer })]);
			})),
		]),
		ngEl("p", { class: "page-hint", text: ngT("Someone with the database can therefore see how large your knowledge base is and when you work on it, but not what any of it says.") }),
	]);
}
