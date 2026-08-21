"use strict";

/* How good a password has to be, and why.
 *
 * This is the only lock on the data. There is no password reset and no copy of
 * the key on the server, so a weak password is not a small mistake — it is the
 * whole of the exposure. That argues for a real check rather than a rule of
 * thumb.
 *
 * What it does not do is impose composition rules. "One capital, one digit, one
 * symbol" pushes people towards P@ssw0rd1 — short, predictable, and weaker than
 * four ordinary words in a row. zxcvbn estimates how many guesses a password
 * would actually survive, matching it against dictionaries, names, keyboard
 * walks, dates, repeats and l33t substitutions, so a passphrase is measured for
 * what it is worth instead of being punished for lacking punctuation.
 *
 * The estimator is ~800 KB, so it is not fetched until someone starts typing.
 */

const NG_MIN_PASSWORD = 12; // NIST's floor, raised a little: nothing recovers this
// zxcvbn scores 0-4. Most sites accept 3; this one asks for 4, because 3 still
// admits things like Summer2026Summer, and the usual argument for settling —
// "they can always reset it" — does not exist here. Four unrelated words clear
// it comfortably, which is the habit worth pushing people towards anyway.
const NG_MIN_SCORE = 4;

let ngZxcvbnLoading = null;

// ngLoadStrength pulls the estimator in on first use.
function ngLoadStrength() {
	if (typeof zxcvbn === "function") return Promise.resolve(zxcvbn);
	if (ngZxcvbnLoading) return ngZxcvbnLoading;
	ngZxcvbnLoading = new Promise(function (resolve, reject) {
		const script = document.createElement("script");
		script.src = "/static/vendor/zxcvbn.min.js";
		script.onload = function () { resolve(zxcvbn); };
		script.onerror = function () { reject(new Error("estimator failed to load")); };
		document.head.appendChild(script);
	});
	return ngZxcvbnLoading;
}

// ngRatePassword returns {ok, score, message, detail}. Whatever else the person
// has typed on the form — their name, their email — is passed in as context,
// because a password built out of those is what gets guessed first.
async function ngRatePassword(password, context) {
	if (!password) return { ok: false, score: 0, message: "", detail: "" };
	if (password.length < NG_MIN_PASSWORD) {
		return {
			ok: false,
			score: 0,
			message: ngT("Too short"),
			detail: ngT("Use at least 12 characters, a few unrelated words is the easiest way."),
		};
	}
	let result;
	try {
		const estimate = await ngLoadStrength();
		result = estimate(password, (context || []).filter(Boolean));
	} catch (err) {
		// Say so rather than pretending to have judged it.
		return {
			ok: password.length >= 20,
			score: 2,
			message: ngT("Could not check strength"),
			detail: ngT("The strength estimator did not load. A long passphrase is still your best move."),
		};
	}
	const labels = [
		ngT("Very weak"), ngT("Weak"), ngT("Fair"), ngT("Strong"), ngT("Very strong"),
	];
	const feedback = result.feedback || {};
	let detail = feedback.warning || "";
	if (!detail && feedback.suggestions && feedback.suggestions.length) {
		detail = feedback.suggestions[0];
	}
	if (result.score === NG_MIN_SCORE - 1 && !detail) {
		detail = ngT("Close. One more word would put this out of reach.");
	}
	if (result.score >= NG_MIN_SCORE && !detail) {
		// zxcvbn's slowest-attack figure: the one that matches how this
		// password is actually stored.
		detail = ngT("Guessing this offline would take about ") +
			result.crack_times_display.offline_slow_hashing_1e4_per_second + ".";
	}
	return { ok: result.score >= NG_MIN_SCORE, score: result.score, message: labels[result.score], detail: detail };
}

/* ---- the meter ---- */

// ngWireStrengthMeter attaches live feedback to a password field. Submitting is
// gated by ngRatePassword in the form handler; this only draws.
function ngWireStrengthMeter(input, host, contextInputs) {
	if (!input || !host) return;
	const bar = document.createElement("div");
	bar.className = "pw-bar";
	const fill = document.createElement("span");
	bar.appendChild(fill);
	const label = document.createElement("p");
	label.className = "pw-label";
	const detail = document.createElement("p");
	detail.className = "pw-detail page-hint";
	host.replaceChildren(bar, label, detail);
	host.hidden = !input.value;

	let run = 0;
	const update = async function () {
		const mine = ++run;
		const context = (contextInputs || []).map(function (el) { return el && el.value; });
		const rating = await ngRatePassword(input.value, context);
		if (mine !== run) return; // a later keystroke already answered
		host.hidden = !input.value;
		host.dataset.score = input.value ? String(rating.score) : "";
		fill.style.width = (input.value ? (rating.score + 1) * 20 : 0) + "%";
		label.textContent = input.value ? rating.message : "";
		detail.textContent = rating.detail || "";
	};

	input.addEventListener("input", update);
	input.addEventListener("focus", function () { ngLoadStrength(); }, { once: true });
	(contextInputs || []).forEach(function (el) {
		if (el) el.addEventListener("input", function () { if (input.value) update(); });
	});
}
