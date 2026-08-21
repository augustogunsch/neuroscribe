"use strict";

/* The PIN: what it is, where it is kept, and how it is proved.
 *
 * Six digits that unlock this device instead of the full password. What is
 * stored here is not the PIN guarding a door — it is one copy of the data key
 * sealed under the PIN, which is the only thing the PIN can open. Locking is
 * in lock.js; this file is everything about the secret itself.
 *
 * It travels, and the distinction is the whole design. A lock that vanished
 * when you signed out, or when the browser cleared its storage, is a lock
 * nobody would bother to set. So each device's PIN is also kept as an ordinary
 * synced record, sealed with the data key exactly as a note is. The server
 * stores that record and cannot read it: opening it needs the data key, which
 * needs the password. Signing back in restores this device's lock by itself,
 * with nothing to re-enter.
 *
 * What must never travel is the copy of the data key wrapped under the PIN.
 * Six digits is a million guesses; the account password is not. Handing the
 * server a blob a million tries would open would quietly make the PIN the
 * cheapest way into the account and the password policy decorative. That blob
 * is rebuilt on the device from the PIN in the synced record and the data key
 * already in memory, and never leaves this browser's storage.
 *
 * Locks are per device, because a phone left on a table and a laptop at home
 * are not equally exposed and may deserve different digits. Identifying a
 * device is where a browser cannot fully deliver: there is no MAC address and
 * nothing like one, deliberately, because a stable hardware id is a permanent
 * supercookie. What is available is an id we mint and store — and storage is
 * the very thing the reader may clear. Fingerprinting the screen and the fonts
 * instead would survive that wipe but change under a browser update, and a
 * lock that stops recognising its own device is worse than one that never
 * claimed to. So:
 *
 *   - a native shell that *can* offer a durable id (the Android app passes one
 *     that outlives clearing app data) is believed first;
 *   - otherwise the id is minted here and kept in localStorage;
 *   - and a device that finds no record under its id adopts the account's most
 *     recent lock and re-stamps it as its own.
 *
 * That last step is what makes clearing storage cost the id rather than the
 * PIN. The digits keep working; only the label the settings page shows for
 * this device is renewed.
 *
 * Be clear about what six digits buys. A million combinations is a lot for
 * someone tapping at your unlocked laptop and very little for someone who has
 * copied this browser's storage onto their own machine and can guess offline.
 * The KDF is deliberately expensive and the record erases itself after ten
 * wrong tries, but that erase runs in this browser: a copied profile does not
 * honour it. The PIN is a convenience lock on top of the password — not a
 * replacement.
 *
 * Setting, changing and removing it each confirm the account password with the
 * server, so someone who finds an unlocked session cannot silently swap the
 * lock or take it off. That confirmation is the one part that needs the
 * network; unlocking never does.
 */

const NG_PIN_STORAGE = "ng-pin";
const NG_DEVICE_STORAGE = "ng-device";
const NG_PIN_LENGTH = 6;
const NG_PIN_MAX_TRIES = 10;
const NG_IDLE_DEFAULT = 15; // minutes

/* ---- which device this is ---- */

// ngNativeDeviceId is what a native shell offers, if one is hosting the page.
// The Android app reads an installation id that survives clearing app data and
// hands it in; a plain browser has nothing equivalent to give.
function ngNativeDeviceId() {
	try {
		const native = window.NeuroscribeNative;
		const id = native && typeof native.deviceId === "function" ? native.deviceId() : "";
		return /^[A-Za-z0-9_-]{4,64}$/.test(String(id || "")) ? String(id) : "";
	} catch (err) {
		return "";
	}
}

function ngDeviceId() {
	const native = ngNativeDeviceId();
	if (native) return native;
	try {
		const kept = localStorage.getItem(NG_DEVICE_STORAGE);
		if (kept && /^[A-Za-z0-9_-]{4,64}$/.test(kept)) return kept;
		const minted = ngB64(ngRandom(9)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
		localStorage.setItem(NG_DEVICE_STORAGE, minted);
		return minted;
	} catch (err) {
		// storage refused (private mode): this device is anonymous for now,
		// which costs it the adoption step and nothing else
		return "";
	}
}

// A name for the settings list, guessed from the user agent. Wrong sometimes,
// and it only ever has to be recognisable enough to tell two devices apart.
function ngDeviceLabel() {
	const ua = navigator.userAgent || "";
	const os = /Android/i.test(ua) ? "Android"
		: /iPhone|iPad|iPod/i.test(ua) ? "iOS"
			: /Mac OS X/i.test(ua) ? "Mac"
				: /Windows/i.test(ua) ? "Windows"
					: /Linux/i.test(ua) ? "Linux" : ngT("This device");
	const browser = ngNativeDeviceId() ? ngT("app")
		: /Edg\//.test(ua) ? "Edge"
			: /OPR\//.test(ua) ? "Opera"
				: /Firefox\//.test(ua) ? "Firefox"
					: /Chrome\//.test(ua) ? "Chrome"
						: /Safari\//.test(ua) ? "Safari" : "";
	return browser ? os + " · " + browser : os;
}

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

/* ---- the synced half ----
 *
 * One record per device, kind "pin", payload sealed with the data key. It holds
 * the digits and the idle timeout — never the wrapped key, which is the part a
 * million guesses would open.
 */

// ngPinRecords returns every device's lock, newest first, opened with the data
// key. Empty when locked or signed out, which is the honest answer: without the
// key there is nothing here to read.
async function ngPinRecords() {
	if (typeof ngAllOfKind !== "function" || ngLocked()) return [];
	const out = [];
	for (const rec of await ngAllOfKind("pin")) {
		const header = await ngOpenHeader(rec);
		if (header && header.pin) out.push(Object.assign({ ref: rec.ref }, header));
	}
	return out.sort(function (a, b) { return String(b.at || "").localeCompare(String(a.at || "")); });
}

async function ngWritePinRecord(ref, header) {
	const payload = await ngSealRecord("pin", header);
	if (ref) {
		const rec = await ngGetAny(ref);
		if (rec) {
			rec.payload = payload;
			rec.deleted = 0;
			await ngPut(rec);
			return ref;
		}
	}
	return (await ngCreate("pin", "", payload)).ref;
}

// ngRestorePin rebuilds this browser's wrapped key from the synced record, so a
// device that has the account's PIN but not its own local copy — freshly signed
// in, or storage cleared — arms itself without asking for the digits again. It
// needs the data key, so it can only run unlocked; that is also why it is safe,
// since anything that can run it could already read the notes.
async function ngRestorePin() {
	try {
		if (ngLocked() || !ngCurrentUser()) return false;
		if (ngPinFor(ngCurrentUser())) return false; // this device is already armed
		const stored = sessionStorage.getItem(NG_DK_STORAGE);
		if (!stored) return false;
		const records = await ngPinRecords();
		if (!records.length) return false;
		const device = ngDeviceId();
		// this device's own lock if it still has an id to find it by; otherwise
		// the account's most recent one, re-stamped below as this device's
		const mine = records.find(function (r) { return device && r.device === device; });
		const adopt = mine || records[0];
		await ngArmPin(adopt.pin, adopt.idle, stored, adopt.ref);
		return true;
	} catch (err) {
		// a lock that cannot be restored must not stop the app from opening
		return false;
	}
}

// ngArmPin is the half of setting a PIN that needs no password: wrap the data
// key under the digits for this browser, and record the same digits for this
// device in the account. Both callers have already earned the right to do it.
async function ngArmPin(pin, idleMinutes, dataKeyB64, ref) {
	const salt = ngB64(ngRandom(16));
	const key = await ngPinKey(pin, salt, NG_PIN_ITERATIONS);
	const idle = typeof idleMinutes === "number" ? idleMinutes : NG_IDLE_DEFAULT;
	const device = ngDeviceId();
	const saved = await ngWritePinRecord(ref, {
		device: device,
		label: ngDeviceLabel(),
		pin: pin,
		idle: idle,
		at: ngNow(),
	});
	ngSavePinRecord({
		v: 1,
		user: ngCurrentUser(),
		device: device,
		ref: saved,
		salt: salt,
		iter: NG_PIN_ITERATIONS,
		wrapped: await ngSeal(key, dataKeyB64),
		idle: idle,
		tries: 0,
	});
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

async function ngPinPost(path, params) {
	// ngCsrfToken rather than the cookie straight: an app resumed from the home
	// screen may not have been given one yet, and sending nothing is a 403.
	const token = typeof ngCsrfToken === "function" ? await ngCsrfToken() : ngCsrf();
	const body = new URLSearchParams(params);
	body.set("csrf_token", token);
	return fetch(path, {
		method: "POST",
		headers: { "Content-Type": "application/x-www-form-urlencoded", "X-CSRF-Token": token },
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
	const existing = ngPinFor(ngCurrentUser());
	await ngArmPin(pin, idleMinutes, stored, existing && existing.ref);
}

// ngRemovePin takes the lock off this device and off the account's copy of it,
// so signing in again does not bring back a PIN the reader just removed.
async function ngRemovePin(password) {
	await ngConfirmPassword(password);
	const rec = ngPinFor(ngCurrentUser());
	const device = ngDeviceId();
	for (const entry of await ngPinRecords()) {
		if ((rec && entry.ref === rec.ref) || (device && entry.device === device)) {
			await ngDelete(entry.ref);
		}
	}
	ngClearPin();
}

// ngForgetPinDevice drops another device's lock from the account — the way a
// phone that was lost stops being able to open its notes with six digits. The
// wrapped key still sits in that device's storage; what this removes is its
// ability to come back after a sign-in, which is what a wiped phone would do.
async function ngForgetPinDevice(ref) {
	await ngDelete(ref);
	const rec = ngPinRecord();
	if (rec && rec.ref === ref) ngClearPin();
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
			// Only this device's copy. The account keeps its record, so the
			// owner signs in with the password and finds the lock intact —
			// while whoever burned the ten tries is left at that same password
			// screen with nothing. Erasing the account copy here would let
			// anyone holding a signed-in device strip the PIN off every other
			// one, which is a way to be robbed of a lock, not to keep it.
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

	const devices = panel.querySelector("[data-pin-devices]");

	// The other devices this account has a lock on. Useful on its own — it is
	// the only place the reader can see that a phone they no longer have will
	// still let six digits in — and it is where they can say so.
	const listDevices = async function () {
		if (!devices) return;
		const here = ngDeviceId();
		const rec = ngPinRecord();
		const entries = await ngPinRecords();
		devices.replaceChildren();
		if (entries.length < 2) return;
		const list = document.createElement("ul");
		list.className = "pin-devices";
		entries.forEach(function (entry) {
			const mine = (rec && entry.ref === rec.ref) || (here && entry.device === here);
			const row = document.createElement("li");
			const name = document.createElement("span");
			name.textContent = (entry.label || ngT("This device")) +
				(mine ? " — " + ngT("this one") : "");
			row.append(name);
			if (!mine) {
				const drop = document.createElement("button");
				drop.type = "button";
				drop.className = "danger";
				drop.textContent = ngT("Forget");
				drop.addEventListener("click", async function () {
					drop.disabled = true;
					await ngForgetPinDevice(entry.ref);
					await listDevices();
				});
				row.append(drop);
			}
			list.append(row);
		});
		const heading = document.createElement("h3");
		heading.className = "pin-devices-title";
		heading.textContent = ngT("Devices with a PIN");
		devices.append(heading, list);
	};

	const refresh = function () {
		const rec = ngPinFor(ngCurrentUser());
		status.textContent = rec ? ngT("set on this device") : ngT("not set on this device");
		status.className = rec ? "run-ok" : "run-bad";
		save.textContent = rec ? ngT("Change PIN") : ngT("Set PIN");
		remove.hidden = !rec;
		lock.hidden = !rec;
		if (rec) idle.value = String(rec.idle);
		listDevices();
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
