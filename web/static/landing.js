/* The landing page shows a real KaTeX-typeset formula in its product shot,
   rather than a picture of one. Kept out of app.js so the marketing page
   stays a single small script. */
document.addEventListener("DOMContentLoaded", function () {
	document.querySelectorAll(".shot-math[data-tex]").forEach(function (el) {
		if (typeof katex === "undefined") {
			el.textContent = el.dataset.tex;
			return;
		}
		try {
			katex.render(el.dataset.tex, el, { displayMode: true, throwOnError: false });
		} catch (e) {
			el.textContent = el.dataset.tex;
		}
	});
});
