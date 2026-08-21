"use strict";

/* Turning a decrypted note into a page.
 *
 * The server cannot read a note, so it cannot render one either: parsing,
 * sanitizing and typesetting all happen here, over text decrypted from the
 * local store.
 */

/* ---- markdown, rendered here because the server cannot read it ---- */

// Math is pulled out before Markdown parsing so underscores and backslashes
// inside formulas survive, then put back as elements KaTeX can typeset.
function ngProtectMath(src) {
	const stash = [];
	const keep = (tex, display) => {
		stash.push({ tex: tex, display: display });
		return "NGXMATH" + (stash.length - 1) + "X";
	};
	let out = src.replace(/\$\$([\s\S]+?)\$\$/g, (_, tex) => keep(tex, true));
	out = out.replace(/(^|[^\\$])\$([^\n$]+?)\$/g, (m, pre, tex) => pre + keep(tex, false));
	return { text: out, stash: stash };
}

function ngRestoreMath(root, stash) {
	const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
	const hits = [];
	while (walker.nextNode()) {
		if (walker.currentNode.nodeValue.indexOf("NGXMATH") !== -1) hits.push(walker.currentNode);
	}
	hits.forEach(function (node) {
		const frag = document.createDocumentFragment();
		node.nodeValue.split(/NGXMATH(\d+)X/).forEach(function (part, i) {
			if (i % 2 === 0) {
				if (part) frag.appendChild(document.createTextNode(part));
				return;
			}
			const item = stash[Number(part)];
			const span = document.createElement("span");
			span.className = "math " + (item.display ? "display" : "inline");
			span.textContent = item.tex;
			frag.appendChild(span);
		});
		node.parentNode.replaceChild(frag, node);
	});
}

// Code blocks are wrapped in the shell the stylesheet and the Run button
// expect: a .codeblock with a header naming the language.
function ngDressCodeBlocks(root) {
	root.querySelectorAll("pre > code").forEach(function (code) {
		const pre = code.parentNode;
		if (pre.closest(".codeblock")) return;
		const match = /language-([a-zA-Z0-9+#._-]+)/.exec(code.className || "");
		const lang = match ? match[1].toLowerCase() : "";
		const block = document.createElement("div");
		block.className = "codeblock";
		block.dataset.lang = lang;
		// the marker set by the fence, moved up where enhance can see it
		if (code.classList.contains("ng-plot")) block.classList.add("is-plot");
		const head = document.createElement("div");
		head.className = "codehead";
		const label = document.createElement("span");
		label.className = "codelang";
		label.textContent = lang || "text";
		head.appendChild(label);
		pre.parentNode.insertBefore(block, pre);
		block.appendChild(head);
		block.appendChild(pre);
	});
}

/* ---- rendering a note ----
 *
 * The server cannot read Markdown, so the whole XSS question lives here.
 * marked passes raw HTML straight through by design, which makes DOMPurify
 * the only thing standing between a note and this page — and this page holds
 * the key that decrypts every other note.
 *
 * So it is configured as an allowlist rather than a list of things to forbid:
 * anything not named below does not survive, including whatever appears in
 * next year's browsers.
 */

// What Markdown can legitimately produce. No div or span: nothing in the
// pipeline emits them, so their only source would be raw HTML in a note.
// No form controls, no svg or math (the classic mutation-XSS namespaces),
// no iframe, object or embed.
const NG_ALLOWED_TAGS = [
	"a", "b", "blockquote", "br", "caption", "code", "dd", "del", "dl", "dt",
	"em", "h1", "h2", "h3", "h4", "h5", "h6", "hr", "i", "img", "ins", "li",
	"ol", "p", "pre", "s", "strong", "sub", "sup", "table", "tbody", "td",
	"tfoot", "th", "thead", "tr", "u", "ul",
];

// No id or name: those let content collide with elements this app looks up
// by name, which is how DOM clobbering starts. No style, so a note cannot
// paint itself over the interface.
const NG_ALLOWED_ATTR = [
	"align", "alt", "colspan", "class", "height", "href", "lang", "reversed",
	"rowspan", "src", "start", "title", "width",
];

// http(s), mailto, and anything relative — which is how images are written.
// Every scheme that can execute (javascript:, data:, vbscript:) falls outside
// it, as does anything DOMPurify cannot recognise.
const NG_SAFE_URI = /^(?:(?:https?|mailto):|[^a-z]|[a-z+.-]+(?:[^a-z+.:-]|$))/i;

// Syntax highlighting needs the language marker Markdown puts on a fence, and
// nothing else: an arbitrary class from a note could borrow this app's own
// styling and dress itself up as part of the interface.
//
// ng-plot joins it as the one marker a fence may set deliberately (see
// NG_FENCE_MARKERS). It is named here rather than pattern-matched so that the
// set of classes a note can put on the page stays a list somebody chose.
const NG_LANG_CLASS = /^language-[A-Za-z0-9+#._-]+$/;
const NG_CODE_MARKS = ["ng-plot"];

let ngPurifyHooked = false;

function ngSanitizer() {
	if (typeof DOMPurify === "undefined") {
		// Fail closed. A note is not worth showing unsanitized.
		throw new Error("the sanitizer did not load, refusing to render");
	}
	if (!ngPurifyHooked) {
		DOMPurify.addHook("afterSanitizeAttributes", function (node) {
			if (node.hasAttribute && node.hasAttribute("class")) {
				const kept = String(node.getAttribute("class")).split(/\s+/)
					.filter((c) => node.tagName === "CODE" &&
						(NG_LANG_CLASS.test(c) || NG_CODE_MARKS.indexOf(c) !== -1));
				if (kept.length) node.setAttribute("class", kept.join(" "));
				else node.removeAttribute("class");
			}
			// A link that replaces this tab could put a convincing sign-in page
			// where the app used to be, so external ones leave in their own.
			if (node.tagName === "A" && /^https?:/i.test(node.getAttribute("href") || "")) {
				node.setAttribute("target", "_blank");
				node.setAttribute("rel", "noopener noreferrer nofollow");
			}
		});
		ngPurifyHooked = true;
	}
	return DOMPurify;
}

/* A fence may carry one extra word: ```python plot.
 *
 * marked keeps the whole info string on the token but writes only the first
 * word into the class, and the marker cannot travel as a data attribute
 * because the sanitizer strips those on purpose. So it travels as a class —
 * and exactly one class, from a fixed list. The info string is note content,
 * and note content does not get to choose what classes appear in the page:
 * anything other than the word below is simply not a marker.
 */
const NG_FENCE_MARKERS = { plot: "ng-plot" };

function ngMarkedRenderer() {
	const renderer = new marked.Renderer();
	// marked 12 passes (code, infostring, escaped) positionally. Taking the
	// arguments through rather than naming them keeps this working if a later
	// version hands over a token object instead: the marker is then simply not
	// found, and a plot fence degrades to an ordinary code block.
	const base = renderer.code.bind(renderer);
	renderer.code = function (code, infostring, escaped) {
		const html = base(code, infostring, escaped);
		const words = String(infostring || "").trim().split(/\s+/).slice(1);
		const marks = words.map(function (w) { return NG_FENCE_MARKERS[w.toLowerCase()]; })
			.filter(Boolean);
		if (!marks.length) return html;
		return html.replace(/<code class="language-([^"]*)"/,
			'<code class="language-$1 ' + marks.join(" ") + '"');
	};
	return renderer;
}

// renderMarkdown turns note text into DOM. The sanitized result is taken as a
// fragment rather than a string: assigning HTML back into innerHTML would make
// the browser re-parse what was just cleaned, which is the window mutation-XSS
// lives in. Nothing here is ever serialized again.
function ngRenderMarkdown(target, source) {
	const protectedSrc = ngProtectMath(source);
	const dirty = marked.parse(protectedSrc.text,
		{ gfm: true, breaks: false, renderer: ngMarkedRenderer() });
	const clean = ngSanitizer().sanitize(dirty, {
		ALLOWED_TAGS: NG_ALLOWED_TAGS,
		ALLOWED_ATTR: NG_ALLOWED_ATTR,
		ALLOWED_URI_REGEXP: NG_SAFE_URI,
		// data-* is not decoration here: app.js dispatches clicks by
		// [data-action], so a note carrying one would be driving the app.
		ALLOW_DATA_ATTR: false,
		ALLOW_ARIA_ATTR: false,
		ALLOW_UNKNOWN_PROTOCOLS: false,
		RETURN_DOM_FRAGMENT: true,
	});
	target.replaceChildren(clean);
	ngRestoreMath(target, protectedSrc.stash);
	ngDressCodeBlocks(target);
	enhance(target); // KaTeX + Run buttons, shared with the rest of the app
}

/* ---- enhance rendered markdown: KaTeX math + run buttons ---- */

/* Equation numbering & references. Display math may carry \label{key}
   (auto-numbered) and/or \tag{x} (explicit tag, wins over the number);
   \eqref{key} / \ref{key} anywhere in math becomes a link to the equation.
   Scope is the enhanced fragment: one chapter page numbers 1, 2, 3… */

function texTagText(tag) {
	return tag.replace(/[\\{}]/g, "");
}

function collectEqLabels(mathEls) {
	var labels = {};
	var num = 0;
	mathEls.forEach(function (el) {
		if (el.dataset.rendered || !el.classList.contains("display")) return;
		var tex = el.textContent;
		var labelM = tex.match(/\\label\{([^}]+)\}/);
		var tagM = tex.match(/\\tag\{([^}]+)\}/);
		if (!labelM && !tagM) return;
		var tag = tagM ? tagM[1] : String(++num);
		if (!tagM) el.dataset.eqTag = tag;
		if (labelM) {
			var id = "eq-" + labelM[1].replace(/[^a-zA-Z0-9_-]/g, "-");
			el.id = id;
			labels[labelM[1]] = { tag: tag, id: id };
		}
	});
	return labels;
}

function enhance(root) {
	if (!(root instanceof Element) && root !== document) return;
	var mathEls = root.querySelectorAll(".math");
	var eqLabels = collectEqLabels(mathEls);
	mathEls.forEach(function (el) {
		if (el.dataset.rendered) return;
		el.dataset.rendered = "1";
		var tex = el.textContent;
		// strip labels, apply auto-number tags, resolve references
		tex = tex.replace(/\\label\{[^}]+\}/g, "");
		if (el.dataset.eqTag) tex += "\\tag{" + texTagText(el.dataset.eqTag) + "}";
		tex = tex.replace(/\\(?:eqref|ref)\{([^}]+)\}/g, function (_, key) {
			var target = eqLabels[key];
			if (!target) return "\\text{(?)}";
			return "\\href{#" + target.id + "}{\\text{(" + texTagText(target.tag) + ")}}";
		});
		if (typeof katex !== "undefined") {
			try {
				katex.render(tex, el, {
					displayMode: el.classList.contains("display"),
					throwOnError: false,
					trust: function (ctx) {
						return ctx.command === "\\href" && ctx.url.charAt(0) === "#";
					},
				});
			} catch (e) {
				el.textContent = tex;
			}
		}
	});
	root.querySelectorAll(".codeblock[data-lang]").forEach(function (block) {
		var lang = runnableLang(block.dataset.lang);
		if (!lang || block.querySelector(".run-btn")) return;
		var head = block.querySelector(".codehead");
		if (!head) return;
		var btn = document.createElement("button");
		btn.type = "button";
		btn.className = "run-btn";
		btn.textContent = "▶ " + ngT("Run");
		btn.addEventListener("click", function () { runSnippet(lang, block, btn); });
		head.appendChild(btn);
		// A block marked as a plot draws itself. A figure that has to be
		// clicked into existence is a demo, not an illustration — and the
		// marker is the author saying they want the picture. Everything else
		// still waits to be asked.
		if (block.classList.contains("is-plot")) ngDrawPlot(lang, block, btn);
	});
}
