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
const NG_LANG_CLASS = /^language-[A-Za-z0-9+#._-]+$/;

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
					.filter((c) => node.tagName === "CODE" && NG_LANG_CLASS.test(c));
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

// renderMarkdown turns note text into DOM. The sanitized result is taken as a
// fragment rather than a string: assigning HTML back into innerHTML would make
// the browser re-parse what was just cleaned, which is the window mutation-XSS
// lives in. Nothing here is ever serialized again.
function ngRenderMarkdown(target, source) {
	const protectedSrc = ngProtectMath(source);
	const dirty = marked.parse(protectedSrc.text, { gfm: true, breaks: false });
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
