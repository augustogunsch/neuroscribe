"use strict";

/* Tests for static/e2e.js — the only two places a password is handled.
 *
 * crypto.js is loaded alongside it rather than stubbed, so what the register
 * form puts in its hidden fields is really openable by the login form: these
 * are the two halves of one protocol, and a mock between them would hide
 * exactly the mistakes that matter.
 *
 * strength.js is stubbed, because how a password is scored is that file's
 * business; what matters here is what happens to a form when the answer is no.
 */

const test = require("node:test");
const assert = require("node:assert");
const { load, fakeStorage, fakeElement, fakeDocument, fakeFetch } = require("./harness");

const CSRF = "csrf-token-value";

function input(value) {
	return fakeElement({ value: value === undefined ? "" : value });
}

function registerForm(fields) {
	const f = Object.assign({ password: "a good long password", confirm: "a good long password" }, fields);
	const form = fakeElement({
		dataset: {
			msgMismatch: "Passwords do not match.",
			msgWeak: "Choose a stronger password.",
		},
	});
	form.selectors = {
		"input[data-e2e-password]": input(f.password),
		"input[data-e2e-password-confirm]": input(f.confirm),
		"[data-pw-meter]": fakeElement({}),
		'input[name="username"]': input("ada"),
		'input[name="email"]': input("ada@example.com"),
		'input[name="kdf_salt"]': input(""),
		'input[name="auth_key"]': input(""),
		'input[name="wrapped_key"]': input(""),
		'button[type="submit"]': fakeElement({}),
	};
	return form;
}

function loginForm(fields) {
	const f = Object.assign({ username: "  ada  ", password: "a good long password" }, fields);
	const form = fakeElement({ dataset: { msgWrong: "Wrong username or password." } });
	form.selectors = {
		'input[name="username"]': input(f.username),
		"input[data-e2e-password]": input(f.password),
		'button[type="submit"]': fakeElement({}),
		'input[name="csrf_token"]': input(CSRF),
	};
	return form;
}

// One context per test: the scripts keep module-level state, and a test that
// inherited another's cached key would be testing the wrong thing.
function setup(opts) {
	const o = opts || {};
	const doc = fakeDocument({
		"form[data-e2e-register]": o.register === null ? undefined : o.register,
		"form[data-e2e-login]": o.login === null ? undefined : o.login,
	});
	if (o.register === null) delete doc.selectors["form[data-e2e-register]"];
	if (o.login === null) delete doc.selectors["form[data-e2e-login]"];
	const window = { location: null };
	const ng = load(["crypto.js", "e2e.js"], {
		document: doc,
		window: window,
		fetch: fakeFetch(o.responses || []),
		sessionStorage: fakeStorage({}),
		// strength.js, stubbed: the rating is this test's input, not its subject
		ngWireStrengthMeter: function (field, meter, hints) {
			ng.meterWiring = { field: field, meter: meter, hints: hints };
		},
		ngRatePassword: async function (password, hints) {
			ng.rated = { password: password, hints: hints };
			return o.rating || { ok: true };
		},
	});
	ng.doc = doc;
	ng.win = window;
	return ng;
}

function errorText(form) {
	const box = form.querySelector(".js-auth-error");
	return box ? box.textContent : null;
}

/* ---- the error box ---- */

test("the error box is created once and then reused", () => {
	const ng = setup({ register: registerForm() });
	const form = ng.doc.querySelector("form[data-e2e-register]");

	ng.ngFormError(form, "first");
	assert.strictEqual(errorText(form), "first");
	assert.strictEqual(form.children.length, 1);
	assert.strictEqual(form.children[0].className, "warn js-auth-error");

	// a second failure replaces the wording rather than stacking another box
	ng.ngFormError(form, "second");
	assert.strictEqual(errorText(form), "second");
	assert.strictEqual(form.children.length, 1);
});

/* ---- registration ---- */

test("a page without a register form wires nothing", () => {
	const ng = setup({ register: null, login: null });
	// both wiring functions run on every page; neither may throw on the other's
	assert.doesNotThrow(() => ng.ngWireRegisterForm());
	assert.doesNotThrow(() => ng.ngWireLoginForm());
});

test("the strength meter is wired to the password and the identity fields", () => {
	const ng = setup({ register: registerForm() });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");
	assert.strictEqual(ng.meterWiring.field, form.querySelector("input[data-e2e-password]"));
	assert.strictEqual(ng.meterWiring.meter, form.querySelector("[data-pw-meter]"));
	// username and email are fed in so the meter can refuse a password made of
	// them
	assert.strictEqual(ng.meterWiring.hints.length, 2);
});

test("a mismatched confirmation never reaches the key derivation", async () => {
	const ng = setup({ register: registerForm({ confirm: "something else" }) });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");

	const ev = await form.dispatch("submit");
	assert.strictEqual(ev.defaultPrevented, true);
	assert.strictEqual(errorText(form), "Passwords do not match.");
	assert.strictEqual(form.submitted, 0);
	assert.strictEqual(form.querySelector('input[name="auth_key"]').value, "");
	assert.strictEqual(ng.rated, undefined); // not even scored
});

test("a weak password is refused, with the rater's reason in front", async () => {
	const ng = setup({
		register: registerForm(),
		rating: { ok: false, detail: "This is a top-10 password." },
	});
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");

	await form.dispatch("submit");
	// there is no password reset, so this check is the only one there will be
	assert.strictEqual(errorText(form), "This is a top-10 password. Choose a stronger password.");
	assert.strictEqual(form.submitted, 0);
	assert.strictEqual(ng.rated.password, "a good long password");
	// Array.from: the array was built inside the vm, so its prototype is not
	// this realm's Array and a strict deep-equal would fail on that alone
	assert.deepStrictEqual(Array.from(ng.rated.hints), ["ada", "ada@example.com"]);
});

test("a weak password with no reason still says so", async () => {
	const ng = setup({ register: registerForm(), rating: { ok: false } });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");

	await form.dispatch("submit");
	assert.strictEqual(errorText(form), "Choose a stronger password.");
	assert.strictEqual(form.submitted, 0);
});

test("registration submits keys, and only keys", async () => {
	const ng = setup({ register: registerForm() });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");

	const ev = await form.dispatch("submit");
	assert.strictEqual(ev.defaultPrevented, true);
	assert.strictEqual(form.submitted, 1);
	assert.strictEqual(form.dataset.ready, "1");

	const salt = form.querySelector('input[name="kdf_salt"]').value;
	const authKey = form.querySelector('input[name="auth_key"]').value;
	const wrapped = form.querySelector('input[name="wrapped_key"]').value;
	assert.strictEqual(ng.ngUnB64(salt).length, 16);
	assert.strictEqual(ng.ngUnB64(authKey).length, 32);
	assert.match(wrapped, /^v1\./);

	// the password itself appears in none of them
	for (const v of [salt, authKey, wrapped]) assert.ok(!v.includes("a good long password"));

	// and what was submitted really is an account: the login half opens it
	const opened = await ng.ngUnlock("a good long password", salt, wrapped);
	assert.strictEqual(opened.authKey, authKey);
	assert.strictEqual(opened.dataKey.length, 32);
});

test("the second submit is the real one and is left alone", async () => {
	const ng = setup({ register: registerForm() });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");

	await form.dispatch("submit");
	const authKey = form.querySelector('input[name="auth_key"]').value;

	// form.submit() re-fires submit in a browser; that pass must not be
	// intercepted, or the form would never leave
	const ev = await form.dispatch("submit");
	assert.strictEqual(ev.defaultPrevented, false);
	assert.strictEqual(form.submitted, 1); // no second interception
	assert.strictEqual(form.querySelector('input[name="auth_key"]').value, authKey);
});

test("a failed derivation re-enables the button and says what went wrong", async () => {
	const ng = setup({ register: registerForm() });
	ng.ngWireRegisterForm();
	const form = ng.doc.querySelector("form[data-e2e-register]");
	ng.ngNewAccountKeys = async () => {
		throw new Error("no entropy source");
	};

	await form.dispatch("submit");
	assert.match(errorText(form), /no entropy source/);
	assert.strictEqual(form.querySelector('button[type="submit"]').disabled, false);
	assert.strictEqual(form.submitted, 0);
	assert.notStrictEqual(form.dataset.ready, "1"); // still submittable
});

/* ---- login ---- */

test("a page without a login form wires nothing", () => {
	const ng = setup({ login: null });
	assert.doesNotThrow(() => ng.ngWireLoginForm());
});

// A registered account, made through the real registration path, for the
// login tests to open.
async function account(ng, password) {
	const keys = await ng.ngNewAccountKeys(password || "a good long password");
	return { salt: keys.salt, wrapped: keys.wrappedKey, authKey: keys.authKey, dataKey: keys.dataKey };
}

test("login proves the password without sending it, then unlocks the tab", async () => {
	const ng = setup({ login: loginForm() });
	const acct = await account(ng);
	ng.fetch = fakeFetch([
		{ json: { salt: acct.salt } },
		{ json: { wrapped_key: acct.wrapped, redirect: "/notes/42" } },
	]);
	ng.ngWireLoginForm();
	const form = ng.doc.querySelector("form[data-e2e-login]");

	await form.dispatch("submit");

	const [params, post] = ng.fetch.calls;
	assert.strictEqual(params.url, "/auth/params?username=ada"); // trimmed
	assert.strictEqual(post.url, "/login");
	assert.strictEqual(post.opts.method, "POST");
	assert.strictEqual(post.opts.headers["X-CSRF-Token"], CSRF);
	assert.strictEqual(post.opts.headers["X-Requested-With"], "neuroscribe");

	const body = post.opts.body.toString();
	assert.ok(body.includes("auth_key=" + encodeURIComponent(acct.authKey).replace(/%20/g, "+")));
	// the password is not in the request in any form
	assert.ok(!body.includes("a good long password"));
	assert.ok(!body.includes(encodeURIComponent("a good long password")));

	// the tab is unlocked with the account's real data key
	assert.strictEqual(ng.sessionStorage.getItem("ng-dk"), ng.ngB64(acct.dataKey));
	assert.strictEqual(ng.win.location, "/notes/42");
	assert.strictEqual(form.querySelector('button[type="submit"]').disabled, false);
});

test("a rejected login shows the server's own page", async () => {
	const ng = setup({ login: loginForm() });
	ng.fetch = fakeFetch([
		{ json: { salt: "AAAAAAAAAAAAAAAAAAAAAA==" } },
		{ ok: false, status: 429, text: "<html>too many attempts</html>" },
	]);
	ng.ngWireLoginForm();
	const form = ng.doc.querySelector("form[data-e2e-login]");

	await form.dispatch("submit");
	// the server knows why (unverified, throttled); do not invent wording
	assert.strictEqual(ng.doc.opened, true);
	assert.strictEqual(ng.doc.written, "<html>too many attempts</html>");
	assert.strictEqual(ng.doc.closed, true);
	assert.strictEqual(ng.sessionStorage.getItem("ng-dk"), null);
	assert.strictEqual(ng.win.location, null);
	assert.strictEqual(form.querySelector('button[type="submit"]').disabled, false);
});

test("a wrapped key that will not open is a wrong password", async () => {
	const ng = setup({ login: loginForm({ password: "not the password" }) });
	const acct = await account(ng);
	ng.fetch = fakeFetch([
		{ json: { salt: acct.salt } },
		{ json: { wrapped_key: acct.wrapped, redirect: "/" } },
	]);
	ng.ngWireLoginForm();
	const form = ng.doc.querySelector("form[data-e2e-login]");

	await form.dispatch("submit");
	// the server accepted the auth key it was given; only the wrapped key can
	// tell this apart, and it is opened here
	assert.strictEqual(errorText(form), "Wrong username or password.");
	assert.strictEqual(ng.sessionStorage.getItem("ng-dk"), null);
	assert.strictEqual(ng.win.location, null);
	assert.strictEqual(form.querySelector('button[type="submit"]').disabled, false);
});

test("a network failure is reported, not swallowed", async () => {
	const ng = setup({ login: loginForm() });
	ng.fetch = fakeFetch([
		() => {
			throw new Error("offline");
		},
	]);
	ng.ngWireLoginForm();
	const form = ng.doc.querySelector("form[data-e2e-login]");

	await form.dispatch("submit");
	assert.match(errorText(form), /offline/);
	assert.strictEqual(form.querySelector('button[type="submit"]').disabled, false);
});

test("only a same-origin redirect is followed", async () => {
	// a compromised backend must not be able to bounce a just-authenticated
	// user to a page that asks for the password again
	const cases = [
		["/notes/42", "/notes/42"],
		["/", "/"],
		["//evil.example", "/"],
		["///evil.example", "/"],
		["https://evil.example", "/"],
		["javascript:alert(1)", "/"],
		["\\\\evil.example", "/"],
		[undefined, "/"],
		[null, "/"],
		["", "/"],
		[42, "/"],
		[{ toString: () => "/notes/1" }, "/"],
	];
	for (const [redirect, want] of cases) {
		const ng = setup({ login: loginForm() });
		const acct = await account(ng);
		ng.fetch = fakeFetch([
			{ json: { salt: acct.salt } },
			{ json: { wrapped_key: acct.wrapped, redirect: redirect } },
		]);
		ng.ngWireLoginForm();
		await ng.doc.querySelector("form[data-e2e-login]").dispatch("submit");
		assert.strictEqual(ng.win.location, want, "redirect " + JSON.stringify(redirect));
	}
});

/* ---- wiring ---- */

test("both forms are wired when the document is ready", async () => {
	const ng = setup({ register: registerForm(), login: loginForm() });
	const register = ng.doc.querySelector("form[data-e2e-register]");
	const login = ng.doc.querySelector("form[data-e2e-login]");
	assert.strictEqual(register.listeners.submit, undefined);

	await ng.doc.dispatch("DOMContentLoaded");

	assert.strictEqual(register.listeners.submit.length, 1);
	assert.strictEqual(login.listeners.submit.length, 1);
});
