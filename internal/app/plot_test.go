package app

// Plots, and the two boundaries they must not cross.
//
// A figure is drawn by running the note's own Python and comes back as SVG.
// Both halves of that are places where this app has already decided something,
// and a plot must not quietly undecide it: the snippet still runs where every
// other snippet runs, and the SVG still arrives as an image rather than as
// markup.

import (
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
