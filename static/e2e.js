"use strict";

/* Signing in and signing up.
 *
 * The only two pages the server still renders, and the only two places a
 * password is handled. Everything past this point reads from the local store:
 * see store.js, model.js and views.js.
 */

/* ---- auth forms ---- */

function ngFormError(form, message) {
	let box = form.querySelector(".js-auth-error");
	if (!box) {
		box = document.createElement("p");
		box.className = "warn js-auth-error";
		form.prepend(box);
	}
	box.textContent = message;
}

// Registration derives the keys here so the password never crosses the wire.
function ngWireRegisterForm() {
	const form = document.querySelector("form[data-e2e-register]");
	if (!form) return;
	ngWireStrengthMeter(
		form.querySelector("input[data-e2e-password]"),
		form.querySelector("[data-pw-meter]"),
		[form.querySelector('input[name="username"]'), form.querySelector('input[name="email"]')]);
	form.addEventListener("submit", async function (e) {
		if (form.dataset.ready === "1") return; // second pass: let it through
		e.preventDefault();
		// these inputs deliberately have no name: an accidental plain submit
		// must not be able to carry the password to the server
		const password = form.querySelector("input[data-e2e-password]").value;
		const confirm = form.querySelector("input[data-e2e-password-confirm]").value;
		if (password !== confirm) {
			ngFormError(form, form.dataset.msgMismatch);
			return;
		}
		// The server never sees this password and cannot judge it, and there
		// is no reset if it turns out to be guessable: this check is the only
		// one there will ever be.
		const rating = await ngRatePassword(password, [
			form.querySelector('input[name="username"]').value,
			form.querySelector('input[name="email"]').value,
		]);
		if (!rating.ok) {
			ngFormError(form, (rating.detail ? rating.detail + " " : "") + form.dataset.msgWeak);
			return;
		}
		const button = form.querySelector('button[type="submit"]');
		button.disabled = true;
		try {
			const keys = await ngNewAccountKeys(password);
			form.querySelector('input[name="kdf_salt"]').value = keys.salt;
			form.querySelector('input[name="auth_key"]').value = keys.authKey;
			form.querySelector('input[name="wrapped_key"]').value = keys.wrappedKey;
			// the password fields never get a name, so they are not submitted
			form.dataset.ready = "1";
			form.submit();
		} catch (err) {
			button.disabled = false;
			ngFormError(form, String(err));
		}
	});
}

// Login proves knowledge of the password without sending it, then unlocks the
// account key in this tab.
function ngWireLoginForm() {
	const form = document.querySelector("form[data-e2e-login]");
	if (!form) return;
	form.addEventListener("submit", async function (e) {
		e.preventDefault();
		const username = form.querySelector('input[name="username"]').value.trim();
		const password = form.querySelector("input[data-e2e-password]").value;
		const button = form.querySelector('button[type="submit"]');
		button.disabled = true;
		try {
			const params = await (await fetch("/auth/params?username=" + encodeURIComponent(username))).json();
			const unlocked = await ngUnlockAttempt(password, params.salt, username, form);
			if (!unlocked) return;
		} catch (err) {
			ngFormError(form, String(err));
		} finally {
			button.disabled = false;
		}
	});
}

async function ngUnlockAttempt(password, salt, username, form) {
	const derived = await ngDeriveKeys(password, salt);
	const body = new URLSearchParams({
		username: username,
		auth_key: derived.authKey,
		csrf_token: form.querySelector('input[name="csrf_token"]').value,
	});
	const resp = await fetch("/login", {
		method: "POST",
		headers: {
			"Content-Type": "application/x-www-form-urlencoded",
			"X-Requested-With": "neuroscribe",
			"X-CSRF-Token": form.querySelector('input[name="csrf_token"]').value,
		},
		body: body,
	});
	if (!resp.ok) {
		// the server rendered a page explaining why (wrong details, unverified,
		// throttled); show it rather than inventing our own wording
		document.open();
		document.write(await resp.text());
		document.close();
		return false;
	}
	const data = await resp.json();
	let dataKey;
	try {
		dataKey = await ngOpen(derived.encKey, data.wrapped_key);
	} catch (err) {
		ngFormError(form, form.dataset.msgWrong);
		return false;
	}
	ngStoreDataKey(ngUnB64(dataKey));
	window.location = data.redirect || "/";
	return true;
}

document.addEventListener("DOMContentLoaded", function () {
	ngWireRegisterForm();
	ngWireLoginForm();
});
