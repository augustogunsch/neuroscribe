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
			return compiler;
		})().catch((err) => {
			bootPromise = null; // a failed boot must not poison later attempts
			throw err;
		});
	}
	return bootPromise;
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
	const { id, source, files } = e.data || {};
	const mapped = [];
	try {
		const compiler = await boot();
		for (const name of Object.keys(files || {})) {
			compiler.mapShadow(name, files[name]);
			mapped.push(name);
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
