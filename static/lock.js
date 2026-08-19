"use strict";

/* The PIN lock.
 *
 * Blanking the screen would be theatre. While the app is open its data key sits
 * in sessionStorage and every decrypted title, chapter and image sits in the
 * DOM, so anything that only covered the page would leave all of it one devtools
 * window away. Locking here means the key genuinely stops existing in a usable
 * form:
 *
 *   - any unsaved editor text is sealed with the data key before it goes;
 *   - the data key is wiped from sessionStorage and from memory;
 *   - the page is reloaded, so the plaintext in the DOM — titles, rendered
 *     Markdown, image blob URLs, even the window title — is replaced by the
 *     ciphertext the server sends;
 *   - what remains on this device is one copy of the data key sealed under the
 *     PIN, which is the only thing the PIN can open.
 *
 * Unlocking is deliberately local: the sealed key lives in this browser and the
 * PIN opens it here, so a device with no signal still opens its own notes. The
 * PIN never reaches the server in any form.
 *
 * Be clear about what six digits buys. A million combinations is a lot for
 * someone tapping at your unlocked laptop and very little for someone who has
 * copied this browser's storage onto their own machine and can guess offline.
 * The KDF is deliberately expensive and the record erases itself after ten wrong
 * tries, but that erase runs in this browser: a copied profile does not honour
 * it. The PIN is a convenience lock on top of the password — not a replacement.
 *
 * Setting, changing and removing the PIN each confirm the account password with
 * the server, so someone who finds an unlocked session cannot silently swap the
 * lock or take it off. That confirmation is the one part that needs the network;
 * unlocking never does.
 */

const NG_PIN_STORAGE = "ng-pin";
const NG_DRAFT_STORAGE = "ng-draft";
const NG_PIN_LENGTH = 6;
const NG_PIN_MAX_TRIES = 10;
const NG_IDLE_DEFAULT = 15; // minutes

/* ---- the stored record ---- */

function ngPinRecord() {
	try {
		const raw = localStorage.getItem(NG_PIN_STORAGE);
		if (!raw) return null;
		const rec = JSON.parse(raw);
		return rec && rec.v === 1 && rec.wrapped ? rec : null;
	} catch (err) {
		return null;
	}
}

function ngSavePinRecord(rec) {
	localStorage.setItem(NG_PIN_STORAGE, JSON.stringify(rec));
}

function ngClearPin() {
	localStorage.removeItem(NG_PIN_STORAGE);
	sessionStorage.removeItem(NG_DRAFT_STORAGE);
}

function ngCurrentUser() {
	return (document.body && document.body.dataset.user) || "";
}

// ngPinFor returns the record only when it belongs to whoever is signed in, so
// a second account on the same browser is never offered someone else's lock.
function ngPinFor(username) {
	const rec = ngPinRecord();
	if (!rec) return null;
	if (rec.user && username && rec.user !== username) return null;
	return rec;
}

function ngIdleMinutes() {
	const rec = ngPinRecord();
	// 0 means "never lock on idle", and 0 is falsy: check for a stored number
	// rather than for truthiness, or "Never" quietly becomes the default.
	if (!rec || typeof rec.idle !== "number") return NG_IDLE_DEFAULT;
	return rec.idle;
}

/* ---- password confirmation (the one online step) ---- */

function ngCsrf() {
	// from the cookie, not a meta tag: the shell is one cached document for
	// every page, and a token baked into it goes stale
	const m = /(?:^|;\s*)ng_csrf=([^;]+)/.exec(document.cookie);
	return m ? m[1] : "";
}

function ngPinPost(path, params) {
	const body = new URLSearchParams(params);
	body.set("csrf_token", ngCsrf());
	return fetch(path, {
		method: "POST",
		headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": ngCsrf() },
		body: body,
	});
}

async function ngPinErrorText(resp) {
	try {
		return (await resp.text()).trim();
	} catch (err) {
		return "";
	}
}

// ngConfirmPassword proves the account password the same way login does: derive
// its auth key from the password and the account salt, and send only that. The
// server checks it against the stored hash. This never carries the PIN.
async function ngConfirmPassword(password) {
	const params = await fetch("/auth/params?username=" + encodeURIComponent(ngCurrentUser()), {
		headers: { "X-Requested-With": "neuroscribe" },
	});
	if (!params.ok) throw new Error(ngT("Could not reach the server."));
	const salt = (await params.json()).salt;
	const authKey = (await ngDeriveKeys(password, salt)).authKey;
	const resp = await ngPinPost("/auth/verify", { password_auth: authKey });
	if (resp.status === 403) throw new Error(ngT("Wrong password."));
	if (!resp.ok) throw new Error((await ngPinErrorText(resp)) || ngT("Could not confirm your password."));
}

/* ---- setting, changing and removing ---- */

// ngSetPin seals the data key currently in use, after the password is confirmed.
// It requires an unlocked app: there is nothing to seal otherwise.
async function ngSetPin(pin, idleMinutes, password) {
	const stored = sessionStorage.getItem(NG_DK_STORAGE);
	if (!stored) throw new Error(ngT("Unlock the app before setting a PIN."));
	await ngConfirmPassword(password);
	const salt = ngB64(ngRandom(16));
	const key = await ngPinKey(pin, salt, NG_PIN_ITERATIONS);
	ngSavePinRecord({
		v: 1,
		user: ngCurrentUser(),
		salt: salt,
		iter: NG_PIN_ITERATIONS,
		wrapped: await ngSeal(key, stored),
		idle: typeof idleMinutes === "number" ? idleMinutes : NG_IDLE_DEFAULT,
		tries: 0,
	});
}

async function ngRemovePin(password) {
	await ngConfirmPassword(password);
	ngClearPin();
}

// ngTryPin opens the record locally — no network. A wrong PIN is counted, and
// the record erases itself before the count could be useful to someone working
// through the space by hand at the keyboard. Returns {ok}, {left} or {gone}.
async function ngTryPin(pin) {
	const rec = ngPinRecord();
	if (!rec) return { gone: true };
	let dataKeyB64;
	try {
		const key = await ngPinKey(pin, rec.salt, rec.iter);
		dataKeyB64 = await ngOpen(key, rec.wrapped);
	} catch (err) {
		rec.tries = (rec.tries || 0) + 1;
		if (rec.tries >= NG_PIN_MAX_TRIES) {
			ngClearPin();
			return { gone: true };
		}
		ngSavePinRecord(rec);
		return { left: NG_PIN_MAX_TRIES - rec.tries };
	}
	rec.tries = 0;
	ngSavePinRecord(rec);
	sessionStorage.setItem(NG_DK_STORAGE, dataKeyB64);
	ngCachedKey = null; // force a re-import from what was just restored
	return { ok: true };
}

/* ---- drafts ---- */

// ngStashDrafts seals whatever is being typed. It is sealed with the data key,
// not the PIN: the key is about to become unreachable without the PIN anyway,
// which makes it exactly the right lock, and it means locking does not need to
// know the PIN.
async function ngStashDrafts() {
	const editors = document.querySelectorAll("textarea[name=\"content\"]");
	if (!editors.length) return;
	const key = await ngDataKey();
	if (!key) return;
	const drafts = [];
	for (let i = 0; i < editors.length; i++) {
		const value = editors[i].value;
		if (!value) continue;
		drafts.push({ at: location.pathname, index: i, blob: await ngSeal(key, value) });
		editors[i].value = "";
	}
	if (drafts.length) sessionStorage.setItem(NG_DRAFT_STORAGE, JSON.stringify(drafts));
}

// ngRestoreDrafts puts them back after an unlock, on the page they came from.
// Entries are kept until they are actually applied: after unlocking, a chapter
// is shown rather than edited, so there is no textarea to receive it until the
// reader opens the editor again.
async function ngRestoreDrafts() {
	const raw = sessionStorage.getItem(NG_DRAFT_STORAGE);
	if (!raw) return;
	let drafts;
	try {
		drafts = JSON.parse(raw);
	} catch (err) {
		sessionStorage.removeItem(NG_DRAFT_STORAGE);
		return;
	}
	const key = await ngDataKey();
	if (!key) return; // still locked; the stash is still needed
	const editors = document.querySelectorAll("textarea[name=\"content\"]");
	const pending = [];
	for (const draft of drafts) {
		const editor = draft.at === location.pathname ? editors[draft.index] : null;
		if (!editor) {
			pending.push(draft);
			continue;
		}
		try {
			editor.value = await ngOpen(key, draft.blob);
			editor.dispatchEvent(new Event("input", { bubbles: true }));
		} catch (err) {
			// a draft that will not open is not worth keeping or reporting
		}
	}
	if (pending.length) sessionStorage.setItem(NG_DRAFT_STORAGE, JSON.stringify(pending));
	else sessionStorage.removeItem(NG_DRAFT_STORAGE);
}

/* ---- locking ---- */

let ngLocking = false;

// ngLockNow is the whole point of this file: after it returns, this tab holds
// nothing that reads a note. It does nothing when no PIN is set — locking then
// would strip the key with no way back short of a full sign-in, which is a way
// to lose access, not to protect it.
async function ngLockNow() {
	if (ngLocking || ngLocked() || !ngPinFor(ngCurrentUser())) return;
	ngLocking = true;
	try {
		await ngStashDrafts();
	} catch (err) { /* locking must not be blocked by a failed stash */ }
	ngForgetDataKey();
	ngCachedKey = null;
	// The reload is what clears the decrypted DOM. Doing it by hand would mean
	// finding every rendered title, body, image URL and document.title, and
	// missing one would defeat the exercise.
	location.reload();
}

// ngForgotPin drops the local lock and hands the session back to the password
// sign-in. Going straight to /login would bounce back here, since the session
// is still valid and the tab still has no key: a loop, not an escape.
async function ngForgotPin() {
	ngClearPin();
	try {
		await ngPinPost("/logout", {});
	} catch (err) { /* navigate regardless */ }
	ngForgetDataKey();
	location.href = "/login";
}

/* ---- the overlay ---- */

function ngLockScreen() {
	const overlay = document.createElement("div");
	overlay.className = "lock-screen";
	overlay.setAttribute("role", "dialog");
	overlay.setAttribute("aria-modal", "true");

	const box = document.createElement("div");
	box.className = "lock-box";

	const brand = document.createElement("div");
	brand.className = "brand lock-brand";
	const logo = document.createElement("img");
	logo.className = "logo";
	logo.src = "/static/logo.svg";
	logo.alt = "";
	brand.append(logo, document.createTextNode("Neuroscribe"));

	const title = document.createElement("h1");
	title.textContent = ngT("Locked");
	const hint = document.createElement("p");
	hint.className = "page-hint";
	hint.textContent = ngT("Enter your PIN to unlock this device.");

	const form = document.createElement("form");
	form.className = "lock-form";
	const input = document.createElement("input");
	input.type = "password";
	input.inputMode = "numeric";
	input.autocomplete = "off";
	input.pattern = "[0-9]*";
	input.maxLength = NG_PIN_LENGTH;
	input.className = "pin-input";
	input.setAttribute("aria-label", ngT("PIN"));
	// six digits is the whole input: submitting on the sixth saves the reach
	// for a button the reader can already see is redundant
	input.addEventListener("input", function () {
		if (/^[0-9]{6}$/.test(input.value)) form.requestSubmit();
	});
	const submit = document.createElement("button");
	submit.type = "submit";
	submit.className = "primary";
	submit.textContent = ngT("Unlock");
	form.append(input, submit);

	const error = document.createElement("p");
	error.className = "warn slim lock-error";
	error.hidden = true;

	const escape = document.createElement("p");
	escape.className = "login-alt";
	const forgot = document.createElement("a");
	forgot.href = "#";
	forgot.textContent = ngT("Forgot your PIN? Sign in with your password");
	forgot.addEventListener("click", function (e) {
		e.preventDefault();
		ngForgotPin();
	});
	escape.appendChild(forgot);

	// handing the machine over shouldn't require knowing the PIN: logging out
	// needs no key, wipes this device's copy, and ends the session
	const leave = document.createElement("p");
	leave.className = "login-alt";
	const logout = document.createElement("a");
	logout.href = "#";
	logout.className = "lock-logout";
	logout.textContent = ngT("Log out");
	logout.addEventListener("click", function (e) {
		e.preventDefault();
		logout.textContent = ngT("Signing out…");
		ngLogout();
	});
	leave.appendChild(logout);

	box.append(brand, title, hint, form, error, escape, leave);
	overlay.appendChild(box);

	const fail = function (message) {
		error.textContent = message;
		error.hidden = false;
		input.value = "";
		input.focus();
	};

	form.addEventListener("submit", async function (e) {
		e.preventDefault();
		const pin = input.value.trim();
		if (!/^[0-9]{6}$/.test(pin)) {
			fail(ngT("The PIN is six digits."));
			return;
		}
		submit.disabled = true;
		submit.textContent = ngT("Unlocking…");
		const result = await ngTryPin(pin);
		submit.disabled = false;
		submit.textContent = ngT("Unlock");
		if (result.ok) {
			// Reload rather than re-run the page's decryption by hand: every
			// renderer already ran with no key and marked its elements done, so
			// a reload just starts the normal path over, now with a key.
			location.reload();
			return;
		}
		if (result.gone) {
			// too many attempts: the sealed key is gone, and only the password
			// can bring this session back
			ngForgotPin();
			return;
		}
		fail(ngT("Wrong PIN. ") + ngT("Attempts left: ") + result.left);
	});

	document.body.classList.add("is-locked");
	document.body.appendChild(overlay);
	input.focus();
	return overlay;
}

/* ---- idle ---- */

let ngIdleTimer = null;
let ngIdleWired = false;

function ngResetIdle() {
	if (!ngIdleTimer) return;
	clearTimeout(ngIdleTimer.handle);
	ngIdleTimer.handle = setTimeout(ngLockNow, ngIdleTimer.ms);
}

// ngStartIdleTimer (re)arms the countdown, and is also how it is turned off:
// choosing "never", or removing the PIN, has to cancel a timer already running.
function ngStartIdleTimer() {
	if (ngIdleTimer) clearTimeout(ngIdleTimer.handle);
	ngIdleTimer = null;
	if (!ngPinFor(ngCurrentUser()) || ngLocked()) return;
	const minutes = ngIdleMinutes();
	if (!minutes) return; // 0: lock only on reopening
	ngIdleTimer = { ms: minutes * 60000, handle: null };
	ngResetIdle();
	if (ngIdleWired) return;
	ngIdleWired = true;
	["mousedown", "keydown", "touchstart", "scroll"].forEach(function (event) {
		document.addEventListener(event, ngResetIdle, { passive: true });
	});
	document.addEventListener("visibilitychange", function () {
		if (!document.hidden) ngResetIdle();
	});
}

/* ---- the settings panel ---- */

function ngWireLockSettings() {
	const panel = document.querySelector("[data-pin-settings]");
	if (!panel) return;
	panel.hidden = false;

	const form = panel.querySelector("[data-pin-form]");
	const pin1 = panel.querySelector("[data-pin-new]");
	const pin2 = panel.querySelector("[data-pin-confirm]");
	const password = panel.querySelector("[data-pin-password]");
	const idle = panel.querySelector("[data-pin-idle]");
	const status = panel.querySelector("[data-pin-status]");
	const error = panel.querySelector("[data-pin-error]");
	const save = panel.querySelector("[data-pin-save]");
	const remove = panel.querySelector("[data-pin-remove]");
	const lock = panel.querySelector("[data-pin-lock]");

	const refresh = function () {
		const rec = ngPinFor(ngCurrentUser());
		status.textContent = rec ? ngT("set on this device") : ngT("not set on this device");
		status.className = rec ? "run-ok" : "run-bad";
		save.textContent = rec ? ngT("Change PIN") : ngT("Set PIN");
		remove.hidden = !rec;
		lock.hidden = !rec;
		if (rec) idle.value = String(rec.idle);
	};

	const fail = function (message) {
		error.textContent = message;
		error.hidden = false;
	};

	form.addEventListener("submit", async function (e) {
		e.preventDefault();
		error.hidden = true;
		if (!/^[0-9]{6}$/.test(pin1.value)) {
			fail(ngT("The PIN is six digits."));
			return;
		}
		if (pin1.value !== pin2.value) {
			fail(ngT("The two PINs do not match."));
			return;
		}
		if (!password.value) {
			fail(ngT("Enter your password to confirm."));
			return;
		}
		save.disabled = true;
		try {
			await ngSetPin(pin1.value, Number(idle.value), password.value);
			pin1.value = pin2.value = password.value = "";
			refresh();
			ngStartIdleTimer();
		} catch (err) {
			fail(String((err && err.message) || err));
		} finally {
			save.disabled = false;
		}
	});

	// Changing only the timeout does not touch the sealed key and is not a
	// secret, so it needs neither the PIN nor the password — and it stays local.
	idle.addEventListener("change", function () {
		const rec = ngPinFor(ngCurrentUser());
		if (!rec) return;
		rec.idle = Number(idle.value);
		ngSavePinRecord(rec);
		ngStartIdleTimer();
	});

	remove.addEventListener("click", async function () {
		error.hidden = true;
		if (!password.value) {
			fail(ngT("Enter your password to confirm."));
			return;
		}
		remove.disabled = true;
		try {
			await ngRemovePin(password.value);
			password.value = "";
			ngStartIdleTimer(); // no record left, so this stops the countdown
			refresh();
		} catch (err) {
			fail(String((err && err.message) || err));
		} finally {
			remove.disabled = false;
		}
	});

	lock.addEventListener("click", ngLockNow);
	refresh();
}

/* ---- entry point ---- */

// Runs on every app page. A tab that has no data key but does have a sealed one
// is exactly the "opened the app again" case, and it is the same code path as an
// idle lock.
function ngInitLock() {
	if (!document.body || !document.body.dataset.app) return;
	const rec = ngPinFor(ngCurrentUser());
	if (rec && ngLocked()) {
		ngLockScreen();
		return; // the settings panel stays unwired until there is a key again
	}
	ngWireLockSettings();
	if (rec) ngStartIdleTimer();
}

if (document.readyState === "loading") {
	document.addEventListener("DOMContentLoaded", ngInitLock);
} else {
	ngInitLock();
}
