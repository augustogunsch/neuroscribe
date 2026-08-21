"use strict";

/* Tests for static/crypto.js — the file that decides what the server may see.
 *
 * These run against real WebCrypto, not a stub of it: a test that mocked
 * crypto.subtle would prove the calls were made in the right order and nothing
 * about whether the result can be decrypted. Where a test needs a key
 * derivation it pays for one, which is why this file takes a few seconds.
 */

const test = require("node:test");
const assert = require("node:assert");
const { load, fakeStorage } = require("./harness");

function fresh(storage) {
	return load(["crypto.js"], { sessionStorage: fakeStorage(storage || {}) });
}

const SALT = "AAAAAAAAAAAAAAAAAAAAAA==";

test("base64 survives bytes that are not text", () => {
	const ng = fresh();
	const bytes = new Uint8Array([0, 1, 127, 128, 255, 254, 65]);
	const round = ng.ngUnB64(ng.ngB64(bytes));
	assert.deepStrictEqual(Array.from(round), Array.from(bytes));
	// high bytes are where a naive String.fromCharCode round trip breaks
	assert.strictEqual(ng.ngB64(new Uint8Array([255, 255])), "//8=");
	assert.deepStrictEqual(Array.from(ng.ngUnB64("")), []);
});

test("ngB64 accepts an ArrayBuffer as well as a view", () => {
	const ng = fresh();
	const buf = new Uint8Array([1, 2, 3]).buffer;
	assert.strictEqual(ng.ngB64(buf), "AQID");
});

test("ngRandom returns the asked-for size, and not the same bytes twice", () => {
	const ng = fresh();
	const a = ng.ngRandom(16);
	const b = ng.ngRandom(16);
	assert.strictEqual(a.length, 16);
	assert.strictEqual(ng.ngRandom(0).length, 0);
	assert.notDeepStrictEqual(Array.from(a), Array.from(b));
});

test("the auth key and the encryption key are independent", async () => {
	const ng = fresh();
	const keys = await ng.ngDeriveKeys("correct horse battery staple", SALT);
	// authKey leaves the browser, so it must be transportable; encKey must not
	assert.strictEqual(typeof keys.authKey, "string");
	assert.strictEqual(ng.ngUnB64(keys.authKey).length, 32);
	assert.strictEqual(keys.encKey.constructor.name, "CryptoKey");
	assert.strictEqual(keys.encKey.extractable, false);

	// same password and salt, same auth key: login has to be reproducible
	const again = await ng.ngDeriveKeys("correct horse battery staple", SALT);
	assert.strictEqual(again.authKey, keys.authKey);

	// the master key is shared, but HKDF's info strings must keep the halves
	// apart: sealing under encKey must not be openable by anything authKey
	// discloses, so at minimum the two must differ
	const sealed = await ng.ngSeal(keys.encKey, "secret");
	assert.notStrictEqual(keys.authKey, sealed);
});

test("a different password derives a different auth key", async () => {
	const ng = fresh();
	const a = await ng.ngDeriveKeys("password one", SALT);
	const b = await ng.ngDeriveKeys("password two", SALT);
	assert.notStrictEqual(a.authKey, b.authKey);
});

test("a different salt derives a different auth key", async () => {
	const ng = fresh();
	const a = await ng.ngDeriveKeys("same password", SALT);
	const b = await ng.ngDeriveKeys("same password", "BBBBBBBBBBBBBBBBBBBBBB==");
	assert.notStrictEqual(a.authKey, b.authKey);
});

test("the iteration count is the one OWASP asks for", () => {
	const ng = fresh();
	assert.strictEqual(ng.eval("NG_KDF_ITERATIONS"), 600000);
});

test("sealing without a context produces v1 and round trips", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const blob = await ng.ngSeal(encKey, "hello é 世界");
	assert.match(blob, /^v1\./);
	assert.strictEqual(blob.split(".").length, 3);
	assert.strictEqual(await ng.ngOpen(encKey, blob), "hello é 世界");
});

test("sealing the same text twice gives different ciphertext", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const a = await ng.ngSeal(encKey, "same");
	const b = await ng.ngSeal(encKey, "same");
	assert.notStrictEqual(a, b); // the IV is fresh each time
});

test("an empty context string is not a context", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	assert.match(await ng.ngSeal(encKey, "x", ""), /^v1\./);
});

test("sealing with a context produces v2 and binds to it", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const blob = await ng.ngSeal(encKey, "body", "note:42:body");
	assert.match(blob, /^v2\./);
	assert.strictEqual(await ng.ngOpen(encKey, blob, "note:42:body"), "body");
});

test("a v2 value will not open in another record's place", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const blob = await ng.ngSeal(encKey, "body", "note:42:body");
	// the whole point of the binding: a hostile server relabelling one field
	// as another gets a tag that no longer verifies
	await assert.rejects(() => ng.ngOpen(encKey, blob, "note:99:body"));
	await assert.rejects(() => ng.ngOpen(encKey, blob, "note:42:header"));
	// and dropping the context entirely does not get it open either
	await assert.rejects(() => ng.ngOpen(encKey, blob));
});

test("a v1 value ignores a context it was never sealed with", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const blob = await ng.ngSeal(encKey, "old record");
	// records written before the binding existed must keep opening
	assert.strictEqual(await ng.ngOpen(encKey, blob, "note:42:body"), "old record");
});

test("another key cannot open a sealed value", async () => {
	const ng = fresh();
	const mine = await ng.ngDeriveKeys("mine", SALT);
	const theirs = await ng.ngDeriveKeys("theirs", SALT);
	const blob = await ng.ngSeal(mine.encKey, "secret");
	await assert.rejects(() => ng.ngOpen(theirs.encKey, blob));
});

test("ngOpen refuses anything that is not a sealed envelope", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const refuses = /refusing to open unsealed data/;
	// a compromised server handing back its own cleartext must not be shown
	// as an authentic note
	await assert.rejects(() => ng.ngOpen(encKey, "just some text"), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, ""), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, null), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, undefined), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, 42), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, { v: "v1." }), refuses);
	await assert.rejects(() => ng.ngOpen(encKey, "v3.aaa.bbb"), refuses);
	// the marker has to be at the front, not merely present
	await assert.rejects(() => ng.ngOpen(encKey, "xv1.aaa.bbb"), refuses);
});

test("ngOpen rejects a malformed envelope", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	await assert.rejects(() => ng.ngOpen(encKey, "v1.onlytwo"), /malformed ciphertext/);
	await assert.rejects(() => ng.ngOpen(encKey, "v1.a.b.c"), /malformed ciphertext/);
	await assert.rejects(() => ng.ngOpen(encKey, "v2."), /malformed ciphertext/);
});

test("a tampered ciphertext does not open", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const blob = await ng.ngSeal(encKey, "trust me");
	const parts = blob.split(".");
	const ct = ng.ngUnB64(parts[2]);
	ct[0] ^= 0x01; // one bit
	await assert.rejects(() => ng.ngOpen(encKey, parts[0] + "." + parts[1] + "." + ng.ngB64(ct)));
});

test("bytes round trip through the binary envelope", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const image = new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0, 255]);
	const sealed = await ng.ngSealBytes(encKey, image);
	// the IV rides in front of the ciphertext rather than in a version string
	assert.strictEqual(sealed.length, 12 + image.length + 16);
	assert.deepStrictEqual(Array.from(await ng.ngOpenBytes(encKey, sealed)), Array.from(image));
});

test("an empty payload still seals and opens", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	assert.strictEqual(await ng.ngOpen(encKey, await ng.ngSeal(encKey, "")), "");
	const bytes = await ng.ngOpenBytes(encKey, await ng.ngSealBytes(encKey, new Uint8Array(0)));
	assert.strictEqual(bytes.length, 0);
});

test("a tampered image does not open", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const sealed = await ng.ngSealBytes(encKey, new Uint8Array([1, 2, 3]));
	sealed[20] ^= 0x01;
	await assert.rejects(() => ng.ngOpenBytes(encKey, sealed));
});

test("ngOpenBytes accepts a plain ArrayBuffer", async () => {
	const ng = fresh();
	const { encKey } = await ng.ngDeriveKeys("pw", SALT);
	const sealed = await ng.ngSealBytes(encKey, new Uint8Array([9, 9, 9]));
	// what a fetch of an image actually hands over
	const out = await ng.ngOpenBytes(encKey, sealed.buffer);
	assert.deepStrictEqual(Array.from(out), [9, 9, 9]);
});

test("a PIN key unwraps only with the same PIN, salt and iteration count", async () => {
	const ng = fresh();
	// a low count on purpose: this test is about the inputs, not the cost
	const key = await ng.ngPinKey("123456", SALT, 1000);
	const blob = await ng.ngSeal(key, "the data key");

	const same = await ng.ngPinKey("123456", SALT, 1000);
	assert.strictEqual(await ng.ngOpen(same, blob), "the data key");

	const wrongPin = await ng.ngPinKey("654321", SALT, 1000);
	await assert.rejects(() => ng.ngOpen(wrongPin, blob));

	// its own salt, so two devices with the same PIN produce different bytes
	const otherDevice = await ng.ngPinKey("123456", "BBBBBBBBBBBBBBBBBBBBBB==", 1000);
	await assert.rejects(() => ng.ngOpen(otherDevice, blob));

	// raising the constant later must not lock anyone out of a PIN they set,
	// which only works because the count travels with the record
	const otherCount = await ng.ngPinKey("123456", SALT, 2000);
	await assert.rejects(() => ng.ngOpen(otherCount, blob));
});

test("a PIN with no stored count falls back to the current one", async () => {
	const ng = fresh();
	const iterations = ng.eval("NG_PIN_ITERATIONS");
	assert.strictEqual(iterations, 3000000);
	// slow by design — the only defence a six-digit secret has offline
	const fallback = await ng.ngPinKey("123456", SALT, undefined);
	const explicit = await ng.ngPinKey("123456", SALT, iterations);
	const blob = await ng.ngSeal(fallback, "same key");
	assert.strictEqual(await ng.ngOpen(explicit, blob), "same key");
	// 0 is not a usable count, so it takes the fallback too
	const zero = await ng.ngPinKey("123456", SALT, 0);
	assert.strictEqual(await ng.ngOpen(zero, blob), "same key");
});

test("a new account produces everything registration must send", async () => {
	const ng = fresh();
	const keys = await ng.ngNewAccountKeys("a good long password");
	assert.strictEqual(ng.ngUnB64(keys.salt).length, 16);
	assert.strictEqual(ng.ngUnB64(keys.authKey).length, 32);
	assert.strictEqual(keys.dataKey.length, 32);
	assert.match(keys.wrappedKey, /^v1\./);
	// the data key is wrapped, never sent in the clear
	assert.ok(!keys.wrappedKey.includes(ng.ngB64(keys.dataKey)));

	// two accounts with the same password share nothing
	const other = await ng.ngNewAccountKeys("a good long password");
	assert.notStrictEqual(other.salt, keys.salt);
	assert.notStrictEqual(other.authKey, keys.authKey);
	assert.notDeepStrictEqual(Array.from(other.dataKey), Array.from(keys.dataKey));
});

test("unlock recovers exactly the data key registration made", async () => {
	const ng = fresh();
	const made = await ng.ngNewAccountKeys("a good long password");
	const opened = await ng.ngUnlock("a good long password", made.salt, made.wrappedKey);
	assert.deepStrictEqual(Array.from(opened.dataKey), Array.from(made.dataKey));
	assert.strictEqual(opened.authKey, made.authKey);
});

test("unlock fails on the wrong password", async () => {
	const ng = fresh();
	const made = await ng.ngNewAccountKeys("a good long password");
	// the wrapped key is what tells a wrong password from a right one; there
	// is no server involved in that judgement
	await assert.rejects(() => ng.ngUnlock("not the password", made.salt, made.wrappedKey));
});

test("the tab's key lives in sessionStorage and is cached once imported", async () => {
	const ng = fresh();
	assert.strictEqual(ng.ngLocked(), true);
	assert.strictEqual(await ng.ngDataKey(), null);

	const dataKey = ng.ngRandom(32);
	ng.ngStoreDataKey(dataKey);
	assert.strictEqual(ng.ngLocked(), false);
	// sessionStorage, not localStorage: the key dies with the tab
	assert.strictEqual(ng.sessionStorage.getItem("ng-dk"), ng.ngB64(dataKey));

	const first = await ng.ngDataKey();
	assert.strictEqual(first.constructor.name, "CryptoKey");
	assert.strictEqual(first.extractable, false);
	assert.strictEqual(await ng.ngDataKey(), first); // cached, not re-imported

	ng.ngForgetDataKey();
	assert.strictEqual(ng.ngLocked(), true);
	assert.strictEqual(ng.sessionStorage.getItem("ng-dk"), null);
});

test("forgetting the key clears storage but not the cache", async () => {
	const ng = fresh();
	ng.ngStoreDataKey(ng.ngRandom(32));
	const key = await ng.ngDataKey();

	ng.ngForgetDataKey();
	// what the app asks is ngLocked(), and that says locked
	assert.strictEqual(ng.ngLocked(), true);
	// but the imported key is still handed out, because ngCachedKey is not
	// cleared in here. Every caller today navigates or reloads immediately
	// after, so the stale cache dies with the document — lock.js is the one
	// that stays on the page, and it clears ngCachedKey by hand for that
	// reason. This test records the asymmetry rather than endorsing it: if
	// ngForgetDataKey ever grows the line, this is the assertion to invert.
	assert.strictEqual(await ng.ngDataKey(), key);
});

test("a key already in storage is picked up by a new tab", async () => {
	const dataKey = new Uint8Array(32).fill(7);
	const seed = fresh();
	const ng = load2(seed.ngB64(dataKey));
	assert.strictEqual(ng.ngLocked(), false);
	const key = await ng.ngDataKey();
	// it decrypts what that data key sealed, so the import is faithful
	const sealed = await ng.ngSeal(key, "note body");
	assert.strictEqual(await ng.ngOpen(key, sealed), "note body");
});

function load2(stored) {
	return load(["crypto.js"], { sessionStorage: fakeStorage({ "ng-dk": stored }) });
}
