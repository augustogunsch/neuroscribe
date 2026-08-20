"use strict";

/* Locking this tab.
 *
 * Blanking the screen would be theatre. While the app is open its data key sits
 * in sessionStorage and every decrypted title, chapter and image sits in the
 * DOM, so anything that only covered the page would leave all of it one
 * devtools window away. Locking here means the key genuinely stops existing in
 * a usable form:
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
 * PIN never reaches the server in any form. The PIN itself — how it is stored,
 * synced and proved — is in pin.js.
 */

const NG_DRAFT_STORAGE = "ng-draft";

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
	// The account's copy goes too, and this is the one place that matters:
	// without it, signing in with the password would helpfully restore the very
	// PIN the reader has just said they cannot remember. Deleting a record only
	// writes a tombstone, so it works from behind the lock, with no data key.
	const rec = ngPinRecord();
	if (rec && rec.ref && typeof ngDelete === "function") {
		try { await ngDelete(rec.ref); } catch (err) { /* leaving is what matters */ }
	}
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
