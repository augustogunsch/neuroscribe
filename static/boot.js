/* Applies the saved sidebar state before first paint. This file is loaded
   synchronously in <head> (no defer) so the sidebar never renders at its
   default size and then jumps — doing it from app.js caused a visible flicker
   on every navigation. Kept as a separate file rather than an inline script
   so the CSP can stay script-src 'self'. */
(function () {
	"use strict";
	try {
		var root = document.documentElement;
		var w = parseInt(localStorage.getItem("ng-sidebar-w"), 10);
		if (w >= 180 && w <= 480) root.style.setProperty("--sidebar-w", w + "px");
		if (localStorage.getItem("ng-sidebar-collapsed") === "1") {
			root.setAttribute("data-sidebar", "collapsed");
		}
		/* dark unless asked otherwise */
		root.setAttribute("data-theme", localStorage.getItem("ng-theme") || "dark");
	} catch (e) {
		/* private mode / storage disabled: fall back to the defaults */
	}

	/* Registering the worker is what turns this from a page into something
	   that still opens on a train. Root scope, so it controls /notes/… too;
	   see shell.go for why it is served from / rather than /static/. */
	if ("serviceWorker" in navigator) {
		window.addEventListener("load", function () {
			navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch(function () {
				/* no worker: the app still works, just not offline */
			});
		});
	}
})();
