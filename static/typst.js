/* Typesetting, in the browser.
 *
 * A PDF is the one export that needs a real typesetting engine, and sending a
 * decrypted note to the server for it would be the only place plaintext ever
 * travelled. So the engine comes here instead: Typst, because it compiles to
 * WebAssembly and LaTeX does not. Notes are typeset where their keys already
 * are, and the server has nothing to leak. Formulas are written in LaTeX —
 * mitex translates them — so the math notation is the one everyone knows.
 *
 * This file builds the Typst source; typst-worker.js compiles it.
 */

/* ---- talking to the typesetter ----
 *
 * The compiler itself runs in static/typst-worker.js, because wasm-bindgen
 * needs 'unsafe-eval' to start and this page must not have it. Everything
 * below turns a note into Typst source — plain string work — and sends that
 * across. The worker sends PDF bytes back.
 */

let ngTypstWorker = null;
let ngTypstSeq = 0;
const ngTypstPending = new Map();

function ngTypstSend(source, files) {
	if (!ngTypstWorker) {
		ngTypstWorker = new Worker("/static/typst-worker.js", { type: "module" });
		ngTypstWorker.onmessage = (e) => {
			const waiting = ngTypstPending.get(e.data.id);
			if (!waiting) return;
			ngTypstPending.delete(e.data.id);
			if (e.data.error) waiting.reject(new Error(e.data.error));
			else waiting.resolve(e.data.pdf);
		};
		ngTypstWorker.onerror = (e) => {
			// a worker that failed to start will never answer anyone
			ngTypstPending.forEach((w) => w.reject(new Error(e.message || "typesetter failed to start")));
			ngTypstPending.clear();
			ngTypstWorker.terminate();
			ngTypstWorker = null;
		};
	}
	const id = ++ngTypstSeq;
	return new Promise((resolve, reject) => {
		// A worker that never loads never answers, and a promise that never
		// settles looks exactly like a slow document. Offline with the
		// typesetter uncached is precisely that case.
		const timer = setTimeout(() => {
			ngTypstPending.delete(id);
			reject(new Error("the typesetter did not respond — it may not be available offline"));
		}, 120000);
		ngTypstPending.set(id, {
			resolve: (v) => { clearTimeout(timer); resolve(v); },
			reject: (e) => { clearTimeout(timer); reject(e); },
		});
		ngTypstWorker.postMessage({ id: id, source: source, files: files });
	});
}

/* ---- emitting Typst source ---- */

// Typst string literal: the safe way to hand arbitrary text to a function.
function ngTypstStr(s) {
	return '"' + String(s == null ? "" : s).replace(/\\/g, "\\\\").replace(/"/g, '\\"') + '"';
}

// Characters Typst reads as markup rather than text. Escaping is per-character
// and total, which is what keeps note content from becoming instructions.
function ngTypstText(s) {
	return String(s == null ? "" : s).replace(/[\\#$*_`\[\]<>@~]/g, (c) => "\\" + c);
}

/* ---- math ----
 *
 * Formulas stay in LaTeX and mitex converts them. Two things it does not know
 * are handled here instead, because Typst does them natively and better:
 * \label marks an equation for reference, and \eqref points back at one.
 */

const NG_MATH_LABEL = /\\(?:label|tag)\{([^}]*)\}/g;
// capturing, so String.split keeps the referenced name
const NG_MATH_REF_SPLIT = /\\(?:eqref|ref)\{([^}]*)\}/;

// Typst labels are bare identifiers, so a LaTeX key like "eq:euler" has to be
// reduced to something Typst will accept as one.
function ngMathLabel(name) {
	const clean = String(name).replace(/[^A-Za-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
	return clean ? "eq-" + clean : "";
}

// Prose, as opposed to a title or a label: the same escaping, plus \eqref{x}
// turned into a real Typst reference so it prints the equation's number.
function ngTypstProse(s) {
	return String(s == null ? "" : s).split(NG_MATH_REF_SPLIT).map((part, i) => {
		if (i % 2 === 0) return ngTypstText(part);
		const label = ngMathLabel(part);
		return label ? "@" + label : "";
	}).join("");
}

function ngTypstMath(tex, display) {
	let label = "";
	const src = tex.replace(NG_MATH_LABEL, (_, name) => {
		label = label || ngMathLabel(name);
		return "";
	}).trim();
	if (!display) return "#mi(" + ngTypstStr(src) + ")";
	// only labelled equations are numbered, matching what the LaTeX export did
	if (label) {
		return "#mitex(" + ngTypstStr(src) + ', numbering: "(1)") <' + label + ">";
	}
	return "#mitex(" + ngTypstStr(src) + ")";
}

/* ---- markdown -> typst ----
 *
 * marked has already parsed this note once to put it on screen, so it parses
 * it here too rather than teaching a second parser the same dialect.
 */

function ngTypstRenderer(ctx) {
	const out = [];

	function inlines(tokens) {
		return (tokens || []).map(inline).join("");
	}

	function inline(t) {
		switch (t.type) {
			case "text":
			case "escape":
				return withMath(t.tokens && t.tokens.length
					? inlines(t.tokens) : ngTypstProse(ngUnescapeEntities(t.text)), t);
			case "strong":
				return "#strong[" + inlines(t.tokens) + "]";
			case "em":
				return "#emph[" + inlines(t.tokens) + "]";
			case "del":
				return "#strike[" + inlines(t.tokens) + "]";
			case "codespan":
				return "#raw(" + ngTypstStr(ngUnescapeEntities(t.text)) + ")";
			case "br":
				return "\\\n";
			case "link":
				return "#link(" + ngTypstStr(t.href) + ")[" + inlines(t.tokens) + "]";
			case "image":
				return image(t);
			case "html":
				return ""; // raw HTML is dropped, as it was under LaTeX
			default:
				return t.tokens ? inlines(t.tokens) : ngTypstText(ngUnescapeEntities(t.text || t.raw || ""));
		}
	}

	// Math was lifted out before parsing, so what survives in a text token is a
	// placeholder. Put the formulas back on the way out.
	function withMath(rendered, token) {
		const raw = token && token.text != null ? String(token.text) : "";
		if (raw.indexOf("NGXMATH") === -1 && rendered.indexOf("NGXMATH") === -1) return rendered;
		return rendered.split(/NGXMATH(\d+)X/).map((part, i) => {
			if (i % 2 === 0) return part;
			const item = ctx.math[Number(part)];
			return item ? ngTypstMath(item.tex, item.display) : "";
		}).join("");
	}

	function image(t) {
		const name = ctx.embed(t.href);
		if (!name) {
			return "#emph[" + ngTypstText("[image: " +
				(ngUnescapeEntities(t.text) || t.href || "") + "]") + "]";
		}
		return "#align(center, image(" + ngTypstStr(name) + "))";
	}

	function blocks(tokens) {
		(tokens || []).forEach(block);
	}

	function block(t) {
		switch (t.type) {
			case "space":
				return;
			case "heading":
				// Chapters are the level-1 headings, and the chapter's own
				// headings are renumbered relative to its shallowest one: a
				// chapter written entirely in ### must read as 1.1, 1.2 — not
				// as 1.0.0.1 with two phantom levels the author never wrote.
				out.push("=".repeat(Math.min(t.depth - ctx.headingShift + 2, 6)) +
					" " + inlines(t.tokens) + "\n");
				return;
			case "paragraph":
				out.push(inlines(t.tokens) + "\n");
				return;
			case "text":
				out.push(t.tokens ? inlines(t.tokens) : ngTypstText(ngUnescapeEntities(t.text)));
				return;
			case "blockquote": {
				const inner = ngTypstRenderer(ctx);
				inner.blocks(t.tokens);
				out.push("#quote(block: true)[\n" + inner.source() + "]\n");
				return;
			}
			case "code":
				out.push("#raw(block: true, lang: " +
					(t.lang ? ngTypstStr(String(t.lang).split(/\s+/)[0]) : "none") +
					", " + ngTypstStr(t.text) + ")\n");
				return;
			case "hr":
				out.push("#line(length: 100%, stroke: 0.5pt + luma(160))\n");
				return;
			case "list":
				list(t);
				return;
			case "table":
				table(t);
				return;
			case "html":
				return; // dropped
			default:
				if (t.tokens) blocks(t.tokens);
		}
	}

	function list(t) {
		const marker = t.ordered ? "+" : "-";
		t.items.forEach((item) => {
			const inner = ngTypstRenderer(ctx);
			inner.blocks(item.tokens);
			// indent the item body so nested blocks stay inside the item
			const body = inner.source().trim().split("\n").join("\n  ");
			const box = item.task ? (item.checked ? "☑ " : "☐ ") : "";
			out.push(marker + " " + box + body + "\n");
		});
		out.push("\n");
	}

	function table(t) {
		const head = t.header || [];
		const cols = Math.max(head.length, ...(t.rows || []).map((r) => r.length), 1);
		const cell = (c) => "[" + (c ? inlines(c.tokens) : "") + "]";
		const parts = ["#table(\n  columns: " + cols + ",\n  stroke: 0.5pt + luma(160),\n"];
		if (head.length) {
			parts.push("  table.header(" + head.map((c) => "[#strong" + cell(c) + "]").join(", ") + "),\n");
		}
		(t.rows || []).forEach((row) => {
			parts.push("  " + row.map(cell).join(", ") + ",\n");
		});
		parts.push(")\n");
		out.push(parts.join(""));
	}

	return {
		blocks: blocks,
		source: () => out.join("\n"),
	};
}

// marked HTML-escapes the text it hands back — an apostrophe arrives as
// &#39; — because its own output is HTML. Typst is not HTML, so every piece of
// marked-derived text is turned back into characters before it is escaped for
// Typst. Only text that came through marked: a note title or a field value is
// the author's own bytes, and a literal &amp; in one must survive as it was.
function ngUnescapeEntities(s) {
	return String(s || "").replace(/&lt;/g, "<").replace(/&gt;/g, ">")
		.replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&amp;/g, "&");
}

function ngMarkdownToTypst(src, ctx) {
	const protectedSrc = ngProtectMath(src || "");
	const base = ctx.math.length;
	protectedSrc.stash.forEach((m) => ctx.math.push(m));
	// placeholders are numbered per chapter; shift them into the document's run
	const text = base === 0 ? protectedSrc.text
		: protectedSrc.text.replace(/NGXMATH(\d+)X/g, (_, n) => "NGXMATH" + (base + Number(n)) + "X");
	const tokens = marked.lexer(text, { gfm: true, breaks: false });
	// the shallowest heading this chapter actually uses becomes the level
	// right under the chapter title; deeper ones keep their relative depth
	let min = Infinity;
	tokens.forEach(function (t) {
		if (t.type === "heading" && t.depth < min) min = t.depth;
	});
	ctx.headingShift = min === Infinity ? 1 : min;
	const renderer = ngTypstRenderer(ctx);
	renderer.blocks(tokens);
	return renderer.source();
}

/* ---- the document ---- */

const NG_TYPST_PREAMBLE = `#import "/mitex/lib.typ": mi, mitex

#set page(paper: "a4", margin: 2.7cm, numbering: "1")
#set text(font: "New Computer Modern", size: 11pt)
#set par(justify: true, leading: 0.62em, spacing: 1.1em)
#show raw: set text(font: "DejaVu Sans Mono", size: 9pt)
#show raw.where(block: true): it => block(
  width: 100%, fill: luma(248), inset: 8pt, radius: 2pt,
  stroke: 0.5pt + luma(200), it,
)
#show link: set text(fill: rgb("#1a4f8a"))
#set heading(numbering: "1.1")
#show heading.where(level: 1): it => {
  pagebreak(weak: true)
  block(above: 0pt, below: 1.2em, text(size: 1.6em, it))
}
#set math.equation(numbering: none)
`;

// What Typst will actually embed. GIF is new here — pdflatex could not take
// one — while WebP still falls back to a placeholder rather than risking the
// whole document on a format the compiler may not know.
const NG_TYPST_IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/svg+xml"];

// ngNoteToTypst builds the source for one decrypted note, and collects the
// image files the compiler will need alongside it.
function ngNoteToTypst(note, opts) {
	const files = {};
	const ctx = {
		math: [],
		embed: (href) => {
			const m = /^\/images\/([A-Za-z0-9_-]+)/.exec(String(href || ""));
			if (!m) return "";
			const img = (note.images || {})[m[1]];
			if (!img || NG_TYPST_IMAGE_TYPES.indexOf(img.mime) === -1) return "";
			const name = "/img/" + m[1] + ngImageExt(img.mime);
			files[name] = ngUnB64(img.data);
			return name;
		},
	};

	const body = [NG_TYPST_PREAMBLE];
	body.push("#set document(title: " + ngTypstStr(note.title || "Untitled") + ")\n");

	// title block, standing in for \maketitle
	body.push("#align(center)[\n" +
		"  #block(text(size: 2em, weight: 700)[" + ngTypstText(note.title || "Untitled") + "])\n" +
		"  #v(0.4em)\n" +
		"  #text(size: 0.9em)[" + ngTypstText(opts && opts.date ? opts.date : "") + "]\n" +
		"]\n#v(1.5em)\n");

	const fields = (note.fields || []).filter((f) => (note.meta || {})[f.key]);
	if (note.description || fields.length || note.type) {
		body.push("#align(center)[");
		if (note.type) {
			body.push("  #smallcaps[" + ngTypstText(note.type) + "]\\\n");
		}
		if (note.description) {
			body.push("  #block(width: 80%)[#emph[" + ngTypstText(note.description) + "]]\n  #v(0.8em)\n");
		}
		if (fields.length) {
			body.push("  #table(columns: 2, stroke: none, align: (right, left),\n");
			fields.forEach((f) => {
				const v = f.type === "checkbox" ? "yes" : note.meta[f.key];
				body.push("    [#strong[" + ngTypstText(f.label) + "]], [" + ngTypstText(v) + "],\n");
			});
			body.push("  )\n");
		}
		body.push("]\n#v(1.5em)\n");
	}

	body.push("#outline(title: " + ngTypstStr(opts && opts.contents ? opts.contents : "Contents") +
		", depth: 1)\n");

	(note.chapters || []).forEach((ch) => {
		body.push("\n= " + ngTypstText(ch.title || "") + "\n");
		body.push(ngMarkdownToTypst(ch.content, ctx));
	});

	return { source: body.join("\n"), files: files };
}

/* ---- compiling ---- */

// ngNotePDF turns one decrypted note into PDF bytes. A formula mitex cannot
// translate would otherwise fail the whole document, so a second attempt drops
// to showing the LaTeX verbatim: an imperfect PDF beats no PDF.
async function ngNotePDF(note, opts) {
	const built = ngNoteToTypst(note, opts);
	try {
		return await ngTypstSend(built.source, built.files);
	} catch (err) {
		return await ngTypstSend(ngTypstWithoutMath(built.source), built.files);
	}
}

// the fallback: every formula becomes literal text instead of an equation
function ngTypstWithoutMath(source) {
	return source.replace(/#(?:mi|mitex)\((("(?:[^"\\]|\\.)*"))(?:, numbering: "\(1\)")?\)(\s*<[^>]*>)?/g,
		(_, str) => "#raw(" + str + ")");
}
