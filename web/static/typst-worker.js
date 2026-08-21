/* The typesetter, kept off the page.
 *
 * Typst's WebAssembly is built by wasm-bindgen, whose glue calls new Function()
 * during start-up. That needs 'unsafe-eval', and granting it to the page would
 * mean granting it to the one document holding the decryption keys and the
 * decrypted notes. So the compiler lives here instead: a module worker served
 * with its own Content-Security-Policy (see handlers.go), which has no DOM, no
 * session cookie, no keys, and can still only talk to this origin.
 *
 * What crosses the boundary is one note's Typst source and its images, by
 * postMessage — in-process, never over the network.
 */

const NG_TYPST = "/typst/";

const NG_TYPST_FONTS = [
	"NewCM10-Regular.otf", "NewCM10-Bold.otf", "NewCM10-Italic.otf",
	"NewCM10-BoldItalic.otf", "NewCMMath-Regular.otf",
	"DejaVuSansMono.ttf", "DejaVuSansMono-Bold.ttf",
];

// mitex translates the LaTeX formulas in notes. It is a Typst package, but it
// is vendored and mapped into the compiler's own filesystem rather than
// resolved through the package registry, so no PDF needs the network.
const NG_MITEX_FILES = [
	"lib.typ", "mitex.typ", "mitex.wasm",
	"specs/mod.typ", "specs/prelude.typ", "specs/latex/standard.typ",
];

let bootPromise = null;
let pdfFormat = 1; // CompileFormatEnum.pdf, replaced with the real value on boot

async function fetchBytes(url) {
	const resp = await fetch(url);
	if (!resp.ok) throw new Error("could not load " + url + " (" + resp.status + ")");
	return new Uint8Array(await resp.arrayBuffer());
}

// boot loads ~33 MB of compiler and fonts, once, on the first PDF anyone asks
// for. Later documents reuse it.
function boot() {
	if (!bootPromise) {
		bootPromise = (async () => {
			const mod = await import(NG_TYPST + "typst.mjs");
			// the output format is an enum, and an unrecognised value silently
			// yields typst.ts's own vector format instead of a PDF
			pdfFormat = (await import(NG_TYPST + "compiler.mjs")).CompileFormatEnum.pdf;
			const fonts = await Promise.all(
				NG_TYPST_FONTS.map((f) => fetchBytes(NG_TYPST + "fonts/" + f)));
			const compiler = mod.createTypstCompiler();
			await compiler.init({
				// typst.ts imports its wasm wrapper by package name, which a
				// browser cannot resolve without an import map. Both hooks
				// below exist to point it at real URLs instead.
				getWrapper: () => import(NG_TYPST + "typst_ts_web_compiler.mjs"),
				getModule: () => NG_TYPST + "typst_ts_web_compiler_bg.wasm",
				// assets: false stops typst.ts reaching for its default fonts
				// over the network; ours are the only ones it gets.
				beforeBuild: [mod.loadFonts(fonts, { assets: false })],
			});
			for (const f of NG_MITEX_FILES) {
				compiler.mapShadow("/mitex/" + f, await fetchBytes(NG_TYPST + "packages/mitex/" + f));
			}
			await mapPackage(compiler, "cetz");
			await mapPackage(compiler, "cetz-plot");
			await mapPackage(compiler, "oxifmt");
			return compiler;
		})().catch((err) => {
			bootPromise = null; // a failed boot must not poison later attempts
			throw err;
		});
	}
	return bootPromise;
}

/* Packages are mapped file by file: the compiler has no filesystem, only the
 * shadow entries we hand it, and CeTZ is a tree of .typ files that import each
 * other by relative path. The manifest lists them so nothing has to be
 * discovered at runtime. */
async function mapPackage(compiler, name) {
	const manifest = await (await fetch(NG_TYPST + "packages/" + name + "/files.json")).json();
	for (const f of manifest) {
		compiler.mapShadow("/" + name + "/" + f,
			await fetchBytes(NG_TYPST + "packages/" + name + "/" + f));
	}
}

/* The renderer turns typst.ts's own vector format into SVG.
 *
 * It is a second wasm module and a second megabyte, loaded only when a note
 * actually contains a drawing — a document that is only ever exported as a PDF
 * never needs it, because the compiler reaches PDF on its own.
 *
 * renderSvg returns a string. Its sibling renderToSvg wants a DOM container,
 * which there is none of in here, and putting one within reach would mean
 * running the renderer on the page — where the policy forbids WebAssembly for
 * good reasons. */
let rendererPromise = null;

function renderer() {
	if (!rendererPromise) {
		rendererPromise = (async () => {
			const mod = await import(NG_TYPST + "typst.mjs");
			const r = mod.createTypstRenderer();
			await r.init({
				getWrapper: () => import(NG_TYPST + "typst_ts_renderer.mjs"),
				getModule: () => NG_TYPST + "typst_ts_renderer_bg.wasm",
			});
			return r;
		})().catch((err) => {
			rendererPromise = null;
			throw err;
		});
	}
	return rendererPromise;
}

// draw compiles one figure and returns it as SVG.
async function draw(compiler, source) {
	compiler.addSource("/main.typ", source);
	const result = await compiler.compile({ mainFilePath: "/main.typ" });
	const vector = result && result.result ? result.result : result;
	if (!(vector instanceof Uint8Array)) {
		const diags = (result && result.diagnostics) || [];
		throw new Error(diags.length ? (diags[0].message || String(diags[0])) : "could not draw");
	}
	/* artifactContent rather than a session we manage: renderSvg opens one
	 * around the call and closes it after, which is the difference between
	 * borrowing wasm memory and having to remember to give it back.
	 *
	 * data_selection is the load-bearing part. Left to itself the renderer
	 * emits four things — the drawing, its definitions, some CSS, and a
	 * script — and the script is not decoration: it builds a selectable text
	 * layer at run time, which means the SVG arrives carrying code. A picture
	 * of a note is not a place to accept code from. Asking for the three parts
	 * that draw is better than deleting the fourth afterwards, because it
	 * cannot be got wrong by a change in how that code is written.
	 */
	const r = await renderer();
	const svg = await r.renderSvg({
		artifactContent: vector,
		data_selection: { body: true, defs: true, css: true, js: false },
	});
	if (typeof svg !== "string" || svg.indexOf("<svg") === -1) {
		throw new Error("the renderer did not return an SVG");
	}
	return flatten(svg);
}

/* The typesetter draws each glyph twice: once as vector outlines, and once
 * again inside a <foreignObject> as HTML, invisible, so that text in a page can
 * be selected and searched.
 *
 * That second layer is why the picture would not appear. An SVG shown through
 * an <img> is rendered in a context with no HTML in it at all, and a document
 * carrying foreignObject is not drawn there — not partially, not without the
 * text: the image fails and nothing is shown. Inlining the SVG instead would
 * mean putting markup built from a note into the page, which is the one thing
 * the sanitizer exists to prevent.
 *
 * So the layer is removed here, where the drawing is made, and what leaves this
 * worker is an SVG an <img> will take. What it costs is selecting the text of a
 * figure. What it keeps is every stroke: the outlines are a separate layer and
 * none of them live in here — an axis label is drawn as paths whether or not it
 * can also be selected.
 */
function flatten(svg) {
	const stripped = svg.replace(/<foreignObject\b[^>]*>[\s\S]*?<\/foreignObject>/g, "");
	// An unbalanced document would mean the shape changed and the pattern is
	// removing more, or less, than it should.
	const left = stripped.indexOf("foreignObject");
	if (left !== -1) {
		throw new Error("foreignObject survived the strip, near: " +
			stripped.slice(Math.max(0, left - 60), left + 80));
	}
	// Nothing executable, ever. The <img> this ends up in would not run it
	// anyway; the point is that no later change quietly makes that the only
	// thing standing between a note and the page holding the keys.
	if (/<script\b/i.test(stripped)) {
		throw new Error("the drawing contains a script and will not be shown");
	}
	return stripped;
}

async function typeset(compiler, source) {
	compiler.addSource("/main.typ", source);
	const result = await compiler.compile({ mainFilePath: "/main.typ", format: pdfFormat });
	// typst.ts reports failures as diagnostics rather than by throwing
	const bytes = result && result.result ? result.result : result;
	if (!(bytes instanceof Uint8Array)) {
		const diags = (result && result.diagnostics) || [];
		throw new Error(diags.length
			? (diags[0].message || String(diags[0]))
			: "typesetting failed");
	}
	// a wrong format is silently accepted upstream, so check what came back
	if (!(bytes[0] === 0x25 && bytes[1] === 0x50 && bytes[2] === 0x44 && bytes[3] === 0x46)) {
		throw new Error("typesetter did not return a PDF");
	}
	return bytes;
}

self.onmessage = async (e) => {
	const { id, source, files, want } = e.data || {};
	const mapped = [];
	try {
		const compiler = await boot();
		for (const name of Object.keys(files || {})) {
			compiler.mapShadow(name, files[name]);
			mapped.push(name);
		}
		if (want === "svg") {
			self.postMessage({ id: id, svg: await draw(compiler, source) });
			return;
		}
		const pdf = await typeset(compiler, source);
		// hand the bytes over rather than copying them
		self.postMessage({ id: id, pdf: pdf }, [pdf.buffer]);
	} catch (err) {
		self.postMessage({ id: id, error: String((err && err.message) || err) });
	} finally {
		const compiler = await bootPromise;
		if (compiler) mapped.forEach((name) => compiler.unmapShadow(name));
	}
};
