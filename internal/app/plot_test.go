package app

// Plots, and the two boundaries they must not cross.
//
// A figure is drawn by running the note's own Python and comes back as SVG.
// Both halves of that are places where this app has already decided something,
// and a plot must not quietly undecide it: the snippet still runs where every
// other snippet runs, and the SVG still arrives as an image rather than as
// markup.

import (
	"regexp"
	"strings"
	"testing"
)

// SVG is markup, and markup that goes into a page as markup is the mutation-XSS
// surface the sanitizer refuses to allow. A figure is an <img> instead, where
// an SVG cannot run script or see this DOM — the same reason images in a note
// are shown that way.
func TestFiguresArriveAsImagesNotMarkup(t *testing.T) {
	run := readAsset(t, "static/run.js")

	if !strings.Contains(run, `new Blob([svg], { type: "image/svg+xml" })`) ||
		!strings.Contains(run, "URL.createObjectURL") {
		t.Error("figures no longer become object URLs; something else is carrying the SVG")
	}
	if !strings.Contains(run, `document.createElement("img")`) {
		t.Error("figures are no longer put on the page as images")
	}
	for _, forbidden := range []string{"innerHTML", "insertAdjacentHTML", "outerHTML"} {
		if strings.Contains(run, forbidden) {
			t.Errorf("run.js reached for %s: an SVG assembled into markup is exactly "+
				"the case the sanitizer exists to prevent", forbidden)
		}
	}

	// And the sanitizer still refuses inline SVG, which is what makes the
	// above the only way in.
	render := readAsset(t, "static/render.js")
	tags := allowedTagsBlock(t, render)
	for _, banned := range []string{`"svg"`, `"math"`, `"foreignObject"`} {
		if strings.Contains(tags, banned) {
			t.Errorf("%s is allowed in note markup again", banned)
		}
	}
}

// The marker a fence may set travels as a class, and note content does not get
// to choose classes: an arbitrary one could borrow this app's styling and dress
// itself up as part of the interface. Exactly one word is a marker.
func TestOnlyTheKnownFenceMarkerSurvives(t *testing.T) {
	render := readAsset(t, "static/render.js")

	if !strings.Contains(render, `const NG_FENCE_MARKERS = { plot: "ng-plot" };`) {
		t.Error("the fence-marker table has changed shape; check it is still a fixed list")
	}
	if !strings.Contains(render, `const NG_CODE_MARKS = ["ng-plot"];`) {
		t.Error("the sanitizer's list of allowed code classes has changed shape")
	}
	// The hook has to consult that list rather than pattern-match, or any
	// class matching some new regexp would ride along.
	if !strings.Contains(render, "NG_CODE_MARKS.indexOf(c) !== -1") {
		t.Error("the class hook no longer checks the marker against a fixed list")
	}
}

// Drawing is running. It has to happen where running already happens — in the
// worker, inside the sandboxed frame — and not on the page, which holds the key
// that decrypts every note.
func TestPlotsRunWhereSnippetsRun(t *testing.T) {
	run := readAsset(t, "static/run.js")
	if !strings.Contains(run, "ngRunSnippet(lang, code, onStatus)") {
		t.Error("plots no longer go through the snippet runner")
	}
	for _, forbidden := range []string{"new Function(", "eval("} {
		if strings.Contains(run, forbidden) {
			t.Errorf("run.js evaluates code on the page (%s); the page's policy forbids it "+
				"and the sandboxed frame exists precisely so it does not have to", forbidden)
		}
	}

	// The backend has to be forced before matplotlib is imported: the one it
	// picks by default under Pyodide draws into a canvas on a page, and there
	// is no page inside a worker.
	runner := readAsset(t, "static/runner.js")
	if !strings.Contains(runner, "MPLBACKEND") || !strings.Contains(runner, "agg") {
		t.Error("the matplotlib backend is no longer forced; figures will fail in the worker")
	}
	// Labels must be outlines, or they vanish when the SVG is rendered
	// somewhere that has never heard of the font it names.
	if !strings.Contains(runner, `rcParams["svg.fonttype"] = "path"`) {
		t.Error("svg.fonttype is no longer 'path'; text in exported figures may disappear")
	}
}

// The reader asked for the picture, not the listing. A figure hides its source
// behind a control, and the PDF must not print what the note folded away.
func TestTheSourceStaysFolded(t *testing.T) {
	run := readAsset(t, "static/run.js")
	if !strings.Contains(run, "source.hidden = true") {
		t.Error("a plot's code is no longer hidden by default")
	}
	if !strings.Contains(run, `document.createElement("figure")`) {
		t.Error("a plot is no longer a figure; it has gone back to being a decorated code block")
	}

	typst := readAsset(t, "static/typst.js")
	if !strings.Contains(typst, "#figure(") {
		t.Error("the PDF no longer emits a real figure")
	}
	// The listing must be skipped for a drawn plot, not merely followed by an
	// image: printing both is what this change exists to stop.
	at := strings.Index(typst, "const drawn = ctx.plots[t.text];")
	if at < 0 {
		t.Fatal("the plot branch in the code case is gone")
	}
	rest := typst[at:]
	fig := strings.Index(rest, "#figure(")
	raw := strings.Index(rest, "#raw(block: true")
	if fig < 0 || raw < 0 || fig > raw {
		t.Error("a drawn plot no longer returns before the raw listing is emitted")
	}
}

// Two seconds of interpreter start-up is the whole cost of a figure; fifty
// milliseconds is the drawing. Paying it before anyone asks is the difference.
func TestPythonIsWarmedBeforeItIsNeeded(t *testing.T) {
	run := readAsset(t, "static/run.js")
	for _, needed := range []string{"ngPrewarmPlots", "ngWarmPython", "requestIdleCallback"} {
		if !strings.Contains(run, needed) {
			t.Errorf("%s is gone; the first figure of a session pays the full start-up", needed)
		}
	}
	// Only devices that draw. Starting a Python interpreter for someone who
	// never plots is a large download in exchange for nothing.
	if !strings.Contains(run, "NG_PLOT_FLAG") || !strings.Contains(run, "dataset.runner") {
		t.Error("the warm-up is no longer conditional; it would run on devices that never plot")
	}
	if !strings.Contains(readAsset(t, "static/router.js"), "ngPrewarmPlots()") {
		t.Error("nothing calls the warm-up at boot")
	}
}

// The typesetter's SVG arrives carrying a script.
//
// Left to itself the renderer emits four parts, and one of them is JavaScript
// that builds a selectable text layer at run time. That is harmless in the page
// it was designed for and would not be harmless here: this SVG is produced from
// a note, and the page that would hold it holds the key to every other note. It
// never reaches the page as markup — it goes into an <img>, which cannot run it
// — but the renderer is also asked not to produce it, so that the <img> is the
// second line of defence rather than the only one.
func TestTheDrawingCarriesNoCode(t *testing.T) {
	worker := readAsset(t, "static/typst-worker.js")

	if !strings.Contains(worker, "data_selection") || !strings.Contains(worker, "js: false") {
		t.Error("the renderer is no longer asked for the drawing only; " +
			"its output would carry a script")
	}
	// And refused outright if one appears anyway.
	if !strings.Contains(worker, "<script") {
		t.Error("a drawing containing a script would be shown rather than refused")
	}
	// foreignObject is HTML inside SVG, and an <img> renders none of a document
	// that contains it — the figure would silently not appear at all.
	if !strings.Contains(worker, "foreignObject") {
		t.Error("the selection layer is no longer removed; figures will not display")
	}
}

// A runtime is served with a year-long, immutable cache, which is right for
// bytes that never change at an address and wrong for these — a package is
// rewritten in place when it is fetched, and a version bump replaces the
// compiler without changing its URL. The directory carries a version, one file
// says what it is, and everything else is asked for with it on the end.
//
// What makes this worth a test is the shape of the failure: a browser holding
// last month's runtime does not look like a caching problem, it looks like the
// new code not working, and it lasts a year.
func TestARuntimeChangeReachesBrowsersThatHaveIt(t *testing.T) {
	server := repoFile(t, "internal/app/runner.go")
	if !strings.Contains(string(server), `path.Base(r.URL.Path) == "manifest.json"`) {
		t.Error("the manifest is cached like everything else, so a new runtime would be invisible")
	}
	if !strings.Contains(string(server), "immutable") {
		t.Error("runtime files are no longer cached at all; every load would refetch megabytes")
	}

	worker := readAsset(t, "static/typst-worker.js")
	if !strings.Contains(worker, "manifest.json") || !strings.Contains(worker, `"?v="`) {
		t.Error("the worker no longer versions the addresses it fetches")
	}
	// typst.ts hands whatever getModule returns straight to
	// WebAssembly.instantiate, so the version has to be known before any URL is
	// built rather than awaited inside the builder.
	if !strings.Contains(worker, "await readVersion();") {
		t.Error("the version is not resolved before the runtime loads")
	}
	if strings.Contains(worker, "getModule: async") {
		t.Error("getModule returns a promise; WebAssembly.instantiate will refuse it")
	}
}

// The same SVG goes into the note and into the PDF. That is the whole reason
// the two agree, so the PDF must take the figure as a file and not re-draw it.
func TestThePDFEmbedsTheSameFigure(t *testing.T) {
	typst := readAsset(t, "static/typst.js")
	if !strings.Contains(typst, "embedSVG") {
		t.Error("the PDF builder can no longer embed a figure")
	}
	if !strings.Contains(typst, `"image/svg+xml"`) {
		t.Error("SVG is no longer among the formats the compiler is given")
	}
	// A figure the compiler refuses must cost the figure, not the document.
	if !strings.Contains(typst, "plots: {}") {
		t.Error("the export no longer falls back to a PDF without figures")
	}
}

// Every CeTZ example in the world opens with `#import "@preview/cetz:0.3.4"`,
// because that is how the package is named everywhere outside this app. Inside
// it there is no registry to name it in — the packages are vendored precisely
// so that typesetting reaches nothing — and the compiler answers a registry
// import with a wasm stack trace that mentions neither the package nor the
// line.
//
// So the import is pointed at the local copy before compiling. What makes this
// worth holding down is that the failure is invisible from the code: the fence
// is correct, the package is present, and the drawing still does not appear.
func TestAPastedExampleWorksAsWritten(t *testing.T) {
	worker := readAsset(t, "static/typst-worker.js")

	if !strings.Contains(worker, "ngLocalise") {
		t.Fatal("registry imports are no longer localised")
	}
	// Both compiles, or a figure works and its PDF does not.
	if n := strings.Count(worker, `addSource("/main.typ", ngLocalise(source))`); n != 2 {
		t.Errorf("%d of 2 compiles localise their imports", n)
	}
	// The registry form binds the package name; the path form binds the file
	// stem, `lib`. Without renaming it back, the `cetz.draw` on the next line
	// of every example stops resolving.
	if !strings.Contains(worker, `'" as ' + name`) {
		t.Error("the import no longer keeps the package's name, so cetz.draw breaks")
	}

	// The table of what is vendored exists twice: here for what people write,
	// and in the fetch script for what the packages themselves import. A name
	// in one and not the other is a drawing that will not draw.
	names := func(src string) map[string]string {
		found := map[string]string{}
		re := regexp.MustCompile(`"([a-z0-9-]+)":\s*"(/[a-z0-9-]+/[^"]+\.typ)"`)
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			found[m[1]] = m[2]
		}
		return found
	}
	fromWorker := names(worker)
	fromScript := names(string(repoFile(t, "scripts/package-manifest.py")))
	if len(fromWorker) == 0 || len(fromScript) == 0 {
		t.Fatal("could not find the entrypoint tables; the shape changed")
	}
	for name, entry := range fromWorker {
		if other, ok := fromScript[name]; !ok {
			t.Errorf("%s is mapped for notes but unknown to the fetch script", name)
		} else if other != entry {
			t.Errorf("%s is mapped at %s for notes and %s for packages", name, entry, other)
		}
	}
	for name := range fromScript {
		if _, ok := fromWorker[name]; !ok {
			t.Errorf("%s is fetched but a note cannot import it", name)
		}
	}

	// The README tells people which version their pasted import will actually
	// get, which only helps while it is true. A version bump that does not
	// reach the prose is worse than no prose: it sends someone looking for a
	// function in the wrong release notes.
	makefile := string(repoFile(t, "Makefile"))
	readme := string(repoFile(t, "README.md"))
	pinned := regexp.MustCompile(`(?m)^([A-Z]+)_VERSION\s*:=\s*([0-9.]+)`)
	for _, m := range pinned.FindAllStringSubmatch(makefile, -1) {
		pkg, version := strings.ToLower(m[1]), m[2]
		switch pkg {
		case "mitex", "cetz", "oxifmt":
		case "cetzplot":
			pkg = "cetz-plot"
		default:
			continue // the compiler and the fonts are not packages
		}
		row := "| " + pkg + " | " + version + " |"
		if !strings.Contains(readme, row) {
			t.Errorf("the Makefile pins %s %s; the README does not say so", pkg, version)
		}
	}
}

// What a failure says. Two kinds arrive: a Python traceback puts its point
// last, under the frames, and a panic out of the compiler puts it first and
// then unwinds through wasm. Taking the last line reads one right and the
// other exactly backwards — it shows an address inside a .wasm file, which
// tells the person nothing about the drawing that failed.
func TestTheReasonIsWhatIsShown(t *testing.T) {
	run := readAsset(t, "static/run.js")
	if !strings.Contains(run, "function ngErrorLine") {
		t.Fatal("errors are no longer trimmed deliberately")
	}
	if strings.Contains(run, `String(message).split("\n").pop()`) {
		t.Error("the last line is shown again, which is a stack frame for a compiler panic")
	}
	if !strings.Contains(run, "wasm-function") {
		t.Error("wasm frames are no longer recognised as frames")
	}
}
