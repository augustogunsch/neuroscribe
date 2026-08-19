"use strict";

/* End-to-end encryption, browser side.
 *
 * The password never leaves this file. From it we derive a master key, and
 * from that two independent keys:
 *
 *   authKey — sent to the server, which only ever stores a bcrypt hash of it.
 *             Proving knowledge of it proves knowledge of the password without
 *             disclosing anything that decrypts data.
 *   encKey  — stays here. It unwraps the account's data key, which is what
 *             actually encrypts notes.
 *
 * Both come out of HKDF with different info strings, so holding authKey tells
 * you nothing about encKey. The data key is random per account and stored only
 * in its wrapped form, so changing a password rewraps it instead of
 * re-encrypting every note.
 *
 * Everything here uses WebCrypto: no vendored crypto library to audit or keep
 * up to date. PBKDF2 is used rather than Argon2id because it is native;
 * the iteration count follows current OWASP guidance.
 */

const NG_KDF_ITERATIONS = 600000;
const NG_DK_STORAGE = "ng-dk";

// A six-digit PIN is a million guesses, so the only thing available is to make
// each guess cost something. Measured on the machine this was written on,
// PBKDF2-SHA256 runs about 12M iterations a second, which puts the whole PIN
// space within an afternoon at 1.2M iterations. Raising it to 3M is not a fix —
// nothing fixes six digits against an offline attacker — but it is the
// difference between minutes and hours, and it still unlocks in well under a
// second here. The real defence is the ten-try limit in lock.js.
const NG_PIN_ITERATIONS = 3000000;

const ngEnc = new TextEncoder();
const ngDec = new TextDecoder();

function ngB64(bytes) {
	let s = "";
	const view = new Uint8Array(bytes);
	for (let i = 0; i < view.length; i++) s += String.fromCharCode(view[i]);
	return btoa(s);
}

function ngUnB64(text) {
	const raw = atob(text);
	const out = new Uint8Array(raw.length);
	for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
	return out;
}

function ngRandom(n) {
	return crypto.getRandomValues(new Uint8Array(n));
}

/* ---- key derivation ---- */

async function ngMasterKey(password, saltB64) {
	const base = await crypto.subtle.importKey("raw", ngEnc.encode(password), "PBKDF2", false, ["deriveBits"]);
	const bits = await crypto.subtle.deriveBits(
		{ name: "PBKDF2", salt: ngUnB64(saltB64), iterations: NG_KDF_ITERATIONS, hash: "SHA-256" },
		base, 256);
	return new Uint8Array(bits);
}

// HKDF-Expand over the master key, so the two derived keys are independent.
async function ngSubKey(master, info, usages) {
	const key = await crypto.subtle.importKey("raw", master, "HKDF", false, ["deriveBits"]);
	const bits = await crypto.subtle.deriveBits(
		{ name: "HKDF", hash: "SHA-256", salt: new Uint8Array(0), info: ngEnc.encode(info) },
		key, 256);
	if (usages === "raw") return new Uint8Array(bits);
	return crypto.subtle.importKey("raw", bits, { name: "AES-GCM" }, false, usages);
}

// deriveKeys turns a password into what the server may see and what it may not.
async function ngDeriveKeys(password, saltB64) {
	const master = await ngMasterKey(password, saltB64);
	const authKey = await ngSubKey(master, "neuroscribe-auth-v1", "raw");
	const encKey = await ngSubKey(master, "neuroscribe-enc-v1", ["encrypt", "decrypt"]);
	return { authKey: ngB64(authKey), encKey: encKey };
}

/* ---- symmetric encryption ---- */

// ngSeal produces "v1.iv.ct" (no binding) or, when an `aad` context string is
// given, "v2.iv.ct" with that context as AES-GCM associated data. Binding the
// record's kind and field into the tag stops a hostile server from serving one
// sealed value in another's place (a chapter body relabelled as a note header,
// note A's body under ref B): the tag no longer verifies out of context.
async function ngSeal(key, plaintext, aad) {
	const iv = ngRandom(12);
	const params = { name: "AES-GCM", iv: iv };
	if (aad) params.additionalData = ngEnc.encode(aad);
	const ct = await crypto.subtle.encrypt(params, key, ngEnc.encode(plaintext));
	return (aad ? "v2." : "v1.") + ngB64(iv) + "." + ngB64(ct);
}

async function ngOpen(key, blob, aad) {
	// A payload with no version marker never came from ngSeal. There is no
	// "plaintext fallback": a compromised server could otherwise hand back
	// cleartext it wrote itself and have it shown as an authentic note. Refuse
	// anything that is not a sealed envelope this key can actually open.
	if (typeof blob !== "string" || (blob.indexOf("v1.") !== 0 && blob.indexOf("v2.") !== 0)) {
		throw new Error("refusing to open unsealed data");
	}
	const parts = blob.split(".");
	if (parts.length !== 3) throw new Error("malformed ciphertext");
	const params = { name: "AES-GCM", iv: ngUnB64(parts[1]) };
	// v2 was sealed with associated data; v1 predates the binding and opens
	// without it, so existing records keep working after an upgrade.
	if (parts[0] === "v2" && aad) params.additionalData = ngEnc.encode(aad);
	const plain = await crypto.subtle.decrypt(params, key, ngUnB64(parts[2]));
	return ngDec.decode(plain);
}

// Binary variants, for images: the same envelope, but bytes in and out
// rather than text.
async function ngSealBytes(key, bytes) {
	const iv = ngRandom(12);
	const ct = await crypto.subtle.encrypt({ name: "AES-GCM", iv: iv }, key, bytes);
	const out = new Uint8Array(12 + ct.byteLength);
	out.set(iv, 0);
	out.set(new Uint8Array(ct), 12);
	return out;
}

async function ngOpenBytes(key, bytes) {
	const view = new Uint8Array(bytes);
	const plain = await crypto.subtle.decrypt(
		{ name: "AES-GCM", iv: view.slice(0, 12) }, key, view.slice(12));
	return new Uint8Array(plain);
}

// ngPinKey derives an unwrapping key from a PIN. Same primitive as the
// password, its own salt so two devices with the same PIN produce different
// ciphertext, and an iteration count passed in from the record rather than
// read from the constant — raising the constant later must not lock anyone out
// of a PIN they already set. This stays in the browser: unlocking never needs
// the network, so a laptop with no signal still opens its own notes.
async function ngPinKey(pin, saltB64, iterations) {
	const base = await crypto.subtle.importKey("raw", ngEnc.encode(pin), "PBKDF2", false, ["deriveBits"]);
	const bits = await crypto.subtle.deriveBits(
		{ name: "PBKDF2", salt: ngUnB64(saltB64), iterations: iterations || NG_PIN_ITERATIONS, hash: "SHA-256" },
		base, 256);
	return crypto.subtle.importKey("raw", bits, { name: "AES-GCM" }, false, ["encrypt", "decrypt"]);
}

/* ---- account setup and unlock ---- */

// newAccountKeys produces everything registration must send: a salt, the
// auth key, and the account's data key wrapped so only the password opens it.
async function ngNewAccountKeys(password) {
	const salt = ngB64(ngRandom(16));
	const { authKey, encKey } = await ngDeriveKeys(password, salt);
	const dataKey = ngRandom(32);
	const wrapped = await ngSeal(encKey, ngB64(dataKey));
	return { salt: salt, authKey: authKey, wrappedKey: wrapped, dataKey: dataKey };
}

// unlock recovers the data key from the wrapped copy the server handed back.
async function ngUnlock(password, saltB64, wrappedKey) {
	const { authKey, encKey } = await ngDeriveKeys(password, saltB64);
	const dataKeyB64 = await ngOpen(encKey, wrappedKey);
	return { authKey: authKey, dataKey: ngUnB64(dataKeyB64) };
}

/* ---- the unlocked key for this tab ---- */

// sessionStorage, not localStorage: the key dies with the tab, and it is never
// sent anywhere — the server has no route to it.
function ngStoreDataKey(dataKey) {
	sessionStorage.setItem(NG_DK_STORAGE, ngB64(dataKey));
}

function ngForgetDataKey() {
	sessionStorage.removeItem(NG_DK_STORAGE);
}

let ngCachedKey = null;

async function ngDataKey() {
	if (ngCachedKey) return ngCachedKey;
	const stored = sessionStorage.getItem(NG_DK_STORAGE);
	if (!stored) return null;
	ngCachedKey = await crypto.subtle.importKey("raw", ngUnB64(stored), { name: "AES-GCM" }, false,
		["encrypt", "decrypt"]);
	return ngCachedKey;
}

// locked reports whether this tab still needs the password.
function ngLocked() {
	return !sessionStorage.getItem(NG_DK_STORAGE);
}
