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

function ngTypstSend(source, files, want) {
	if (!ngTypstWorker) {
		ngTypstWorker = new Worker("/static/typst-worker.js", { type: "module" });
		ngTypstWorker.onmessage = (e) => {
			const waiting = ngTypstPending.get(e.data.id);
			if (!waiting) return;
			ngTypstPending.delete(e.data.id);
			if (e.data.error) waiting.reject(new Error(e.data.error));
			else waiting.resolve(e.data.svg !== undefined ? e.data.svg : e.data.pdf);
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
		ngTypstWorker.postMessage({ id: id, source: source, files: files, want: want });
	});
}

/* ---- CeTZ figures ----
 *
 * A ```plot fence is Typst, drawn by the same compiler that typesets the PDF.
 * That is the whole reason to have it: the figure in the note and the figure
 * in the document are not two renderings that agree, they are one rendering.
 *
 * The preamble is deliberately small. It puts cetz and cetz-plot within reach,
 * gives the page no margin and no size of its own so the drawing decides its
 * own bounds, and gets out of the way.
 */
const NG_CETZ_PREAMBLE = `#import "/cetz/src/lib.typ" as cetz: draw, canvas
#import "/cetz-plot/src/lib.typ": plot, chart

#set page(width: auto, height: auto, margin: 4pt, fill: none)
#set text(font: "New Computer Modern", size: 10pt)

`;

// ngCetzFigure draws one fence and returns SVG. The directives a fence may
// carry are comments in Typst too, so the source stays something you could
// paste into a .typ file.
async function ngCetzFigure(code) {
	return ngTypstSend(NG_CETZ_PREAMBLE + "#" + ngCetzBlock(code), {}, "svg");
}

/* One fence, as one Typst expression — used by the note and by the PDF.
 *
 * They must agree, and they are reached by different routes: the note wraps
 * the block in a document of its own and draws it, the PDF drops it into
 * #figure() among the prose. When each decided for itself how to wrap a body,
 * they diverged, and the PDF refused every drawing that opened with an import.
 *
 * The decision is which mode the body is written in. A drawing is normally a
 * sequence of expressions — canvas({ … }) — and has to be put into code mode
 * explicitly, or markup mode typesets the source instead of running it and you
 * get a figure that is a picture of your own code. But every published CeTZ
 * example opens with #import, and a "#" is what markup uses to enter code: in
 * code mode it is a syntax error. So a body that starts with one is markup and
 * goes in a content block; anything else goes in a code block.
 *
 * The directives above the body are comments in Typst, in either mode, so they
 * stay where they were written.
 */
function ngCetzBlock(code) {
	const lines = String(code || "").split("\n");
	let at = 0;
	while (at < lines.length && (!lines[at].trim() || /^\s*(?:#|\/\/)\s*:/.test(lines[at]))) at++;
	const head = lines.slice(0, at).join("\n");
	const body = lines.slice(at).join("\n").trim();
	if (!body) return "[]";
	const open = body.charAt(0) === "#" ? "[" : "{";
	const close = open === "[" ? "]" : "}";
	return open + "\n" + head + "\n" + body + "\n" + close;
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

// Typst labels are bare identifiers, and a figure's label comes from a note,
// so it is reduced to something Typst will accept and cannot collide with the
// equation labels living in the same namespace.
function ngFigureLabel(name) {
	const clean = String(name).replace(/[^A-Za-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
	return clean ? "fig-" + clean : "";
}

// A figure is referred to in the prose as @label. Splitting on it before the
// escaping matters: ngTypstText escapes "@", so anything left to that stage
// would print the label instead of pointing at the picture.
const NG_FIG_REF_SPLIT = /@([A-Za-z0-9_-]{1,64})\b/;

// Prose, as opposed to a title or a label: the same escaping, plus \eqref{x}
// and @figure turned into real Typst references so they print the number.
function ngTypstProse(s, figures) {
	return String(s == null ? "" : s).split(NG_MATH_REF_SPLIT).map((part, i) => {
		if (i % 2 === 0) return ngTypstFigRefs(part, figures);
		const label = ngMathLabel(part);
		return label ? "@" + label : "";
	}).join("");
}

function ngTypstFigRefs(text, figures) {
	if (!figures || !Object.keys(figures).length) return ngTypstText(text);
	return String(text).split(NG_FIG_REF_SPLIT).map((part, i) => {
		if (i % 2 === 0) return ngTypstText(part);
		// only a label this note actually defines is a reference; anything
		// else was an "@" the author meant literally
		return figures[part] ? "@" + ngFigureLabel(part) : ngTypstText("@" + part);
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
					? inlines(t.tokens)
					: ngTypstProse(ngUnescapeEntities(t.text), ctx.figures), t);
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
			case "code": {
				// A fence marked as a plot is a figure, not a listing: the
				// picture is what the note shows and the code is folded away
				// there, so printing the source here would put in the document
				// the one thing the reader chose to hide.
				// A CeTZ fence is Typst: it goes into the document as source and
				// is typeset with everything around it. No image, no embedding,
				// no second rendering that has to agree with the first — the
				// figure in the PDF is drawn by the compiler building the PDF.
				if (String(t.lang || "").trim().split(/\s+/)[0] === "plot") {
					const meta = ngPlotMeta(t.text);
					// wrapped by the same rule the note draws it under, so the
					// two cannot disagree about what mode the body is in
					out.push("#figure(\n  " + ngCetzBlock(t.text) +
						(meta.caption ? ",\n  caption: [" + ngTypstProse(meta.caption, ctx.figures) + "]" : "") +
						"\n)" + (meta.label ? " <" + ngFigureLabel(meta.label) + ">" : "") + "\n");
					return;
				}

				const drawn = ctx.plots[t.text];
				if (drawn && drawn.length) {
					const meta = ngPlotMeta(t.text);
					drawn.forEach(function (svg) {
						const name = ctx.embedSVG(svg);
						if (!name) return;
						out.push("#figure(\n  image(" + ngTypstStr(name) + ", width: 85%)" +
							(meta.caption ? ",\n  caption: [" + ngTypstProse(meta.caption) + "]" : "") +
							"\n)" + (meta.label ? " <" + ngFigureLabel(meta.label) + ">" : "") + "\n");
					});
					return;
				}
				out.push("#raw(block: true, lang: " +
					(t.lang ? ngTypstStr(String(t.lang).split(/\s+/)[0]) : "none") +
					", " + ngTypstStr(t.text) + ")\n");
				return;
			}
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
#import "/cetz/src/lib.typ" as cetz: draw, canvas
#import "/cetz-plot/src/lib.typ": plot, chart

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
	let plotSeq = 0;
	const ctx = {
		math: [],
		plots: (opts && opts.plots) || {},
		// which labels this note defines, so a stray "@" in prose stays an "@"
		figures: (opts && opts.figures) || {},
		// Figures are not note images: they have no record and no address,
		// they were drawn a moment ago from the note's own code. They still
		// reach the compiler the same way, as a file beside the document.
		embedSVG: (svg) => {
			if (typeof svg !== "string" || !svg) return "";
			const name = "/img/plot-" + (++plotSeq) + ".svg";
			files[name] = new TextEncoder().encode(svg);
			return name;
		},
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
/* ngCollectPlots draws every plot fence in a note, ahead of the conversion.
 *
 * The Markdown walker is synchronous and drawing is not — it is a round trip
 * to an interpreter in a sandboxed frame — so the figures are gathered first
 * and the walker only looks them up. Anything already drawn on screen comes
 * straight from the session cache, which is the common case: the usual reason
 * to export a note is that you were just reading it.
 */
async function ngCollectPlots(note, onStatus) {
	const plots = {};
	for (const chapter of note.chapters || []) {
		let tokens;
		try {
			tokens = marked.lexer(String(chapter.content || ""), { gfm: true });
		} catch (err) {
			continue;
		}
		for (const t of tokens) {
			if (t.type !== "code" || plots[t.text]) continue;
			const words = String(t.lang || "").trim().split(/\s+/);
			const lang = typeof runnableLang === "function" ? runnableLang(words[0]) : "";
			if (!lang || words.indexOf("plot") === -1) continue;
			if (typeof ngPlotFigures !== "function") continue;
			const figures = await ngPlotFigures(lang, t.text, onStatus);
			// a plot that will not draw leaves the code in the PDF and no
			// picture, which is the same thing the note shows
			if (Array.isArray(figures) && figures.length) plots[t.text] = figures;
		}
	}
	return plots;
}

// ngPlotLabels is the set of labels the note's figures define. A reference to
// anything else is left as written — an "@" in prose is usually just an "@".
function ngPlotLabels(note, plots) {
	const labels = {};
	Object.keys(plots || {}).forEach(function (code) {
		const meta = ngPlotMeta(code);
		if (meta.label) labels[meta.label] = true;
	});
	// CeTZ fences never go through ngCollectPlots — nothing draws them ahead of
	// time, the compiler does it in place — so their labels are read here.
	for (const chapter of note.chapters || []) {
		let tokens;
		try {
			tokens = marked.lexer(String(chapter.content || ""), { gfm: true });
		} catch (err) {
			continue;
		}
		for (const t of tokens) {
			if (t.type !== "code") continue;
			if (String(t.lang || "").trim().split(/\s+/)[0] !== "plot") continue;
			const meta = ngPlotMeta(t.text);
			if (meta.label) labels[meta.label] = true;
		}
	}
	return labels;
}

async function ngNotePDF(note, opts) {
	const plots = await ngCollectPlots(note, opts && opts.onStatus);
	const figures = ngPlotLabels(note, plots);
	const withPlots = ngNoteToTypst(note,
		Object.assign({}, opts, { plots: plots, figures: figures }));
	try {
		return await ngTypstSend(withPlots.source, withPlots.files);
	} catch (err) {
		// Every formula becomes literal text: a note whose maths the compiler
		// refuses is still worth having as a PDF.
		try {
			return await ngTypstSend(ngTypstWithoutMath(withPlots.source), withPlots.files);
		} catch (err2) {
			// And a figure the compiler will not take must not cost the whole
			// document either. The compiler parses the SVG rather than passing
			// it through, so a malformed one fails the export outright — the
			// note is worth more than the picture.
			if (!Object.keys(plots).length) throw err2;
			const plain = ngNoteToTypst(note,
				Object.assign({}, opts, { plots: {}, figures: {} }));
			return await ngTypstSend(ngTypstWithoutMath(plain.source), plain.files);
		}
	}
}

// the fallback: every formula becomes literal text instead of an equation
function ngTypstWithoutMath(source) {
	return source.replace(/#(?:mi|mitex)\((("(?:[^"\\]|\\.)*"))(?:, numbering: "\(1\)")?\)(\s*<[^>]*>)?/g,
		(_, str) => "#raw(" + str + ")");
}
