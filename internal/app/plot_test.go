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

// A dollar sign inside code is a dollar sign.
//
// Math is lifted out of a note before Markdown is parsed, so that underscores
// and backslashes in a formula survive. Run over the whole source it also
// lifted formulas out of code — and a drawing is code that is read back off
// the page as text, so `$alpha$` in a CeTZ fence came back as KaTeX's markup
// flattened into one word, and the typesetter was asked for a variable called
// alphaalphaalpha. The PDF got the placeholder itself.
//
// Nothing about that is visible from either end: the fence is correct and the
// compiler is fine. It is the pass in between that has to know where code is.
func TestADollarInCodeIsADollar(t *testing.T) {
	render := readAsset(t, "static/render.js")

	if !strings.Contains(render, "function ngOutsideCode") {
		t.Fatal("the math pass no longer knows where code is")
	}
	if !strings.Contains(render, "ngOutsideCode(String(src), prose)") {
		t.Error("ngProtectMath runs over the whole source again, code included")
	}
	// Fences and inline spans both, since `$HOME` in prose is a variable too.
	// Checked by its use, not its name: a constant that is merely defined
	// protects nothing.
	if !strings.Contains(render, "text.split(NG_CODE_SPAN)") {
		t.Error("inline code spans are no longer protected")
	}
}

// The note and the PDF must wrap a figure the same way, because they are the
// same figure — that is the whole claim CeTZ fences make here.
//
// They are reached differently: the note builds a document around the fence
// and draws it, the PDF drops it into #figure() among the prose. While each
// decided for itself how to wrap the body, they disagreed, and the PDF refused
// every drawing that opened with an import — which is every published example.
func TestTheNoteAndThePDFWrapAFigureTheSameWay(t *testing.T) {
	typst := readAsset(t, "static/typst.js")

	if !strings.Contains(typst, "function ngCetzBlock") {
		t.Fatal("the shared wrapping rule is gone")
	}
	// Defined once, used by both paths.
	if n := strings.Count(typst, "ngCetzBlock("); n < 3 {
		t.Errorf("ngCetzBlock has %d mentions; one of the two paths no longer uses it", n)
	}
	if strings.Contains(typst, `"#figure(\n  {" + t.text`) {
		t.Error("the PDF wraps the fence itself again, in code mode, " +
			"where a leading # is a syntax error")
	}
	// A body starting with "#" is markup and must go in a content block; the
	// distinction is the entire point of the function.
	if !strings.Contains(typst, `body.charAt(0) === "#" ? "[" : "{"`) {
		t.Error("the markup-versus-code decision is gone; one of the two forms will fail")
	}
}

// The manual on the index page is the only instructions most people read, and
// it named one engine after there were two — telling someone to write a Python
// fence on a server whose fast path is the typesetter.
//
// It also has to match what the server can do. Each engine needs its own
// runtime, and the page is told which are present; promising a feature that is
// not installed is an instruction ending in a shrug.
func TestTheManualDescribesTheEnginesThisServerHas(t *testing.T) {
	views := readAsset(t, "static/views.js")

	// Every check below matches through to a delimiter — a "(", a quote, a
	// colon. strings.Contains is happy with any superstring, so an assertion
	// that stops at a name passes when that name is renamed or extended, and
	// three of these did exactly that until the mutations caught them.
	if !strings.Contains(views, "function ngPlotEngines()") {
		t.Fatal("the help no longer asks which engines exist")
	}

	// Each engine gated on its own flag: data-typst is the typesetter,
	// data-runner is Python. One standing in for the other is how the help
	// came to offer matplotlib on a server that had only the typesetter.
	for _, pair := range [][2]string{
		{"cetz: !!document.body.dataset.typst", "the typesetter"},
		{"python: !!document.body.dataset.runner", "Python"},
	} {
		if !strings.Contains(views, pair[0]) {
			t.Errorf("%s is no longer gated on its own runtime flag", pair[1])
		}
	}

	// One sample per engine present, and both when both — offering only the
	// typesetter's fence hid Python from every server that had it, which is
	// what this half exists to prevent. Each push guarded by its own flag.
	samples := views
	if at := strings.Index(views, "function ngPlotSamples()"); at >= 0 {
		if end := strings.Index(views[at:], "\n}"); end > 0 {
			samples = views[at : at+end]
		}
	} else {
		t.Fatal("the samples are no longer built per engine")
	}
	for _, guard := range []string{"if (have.cetz) {", "if (have.python) {"} {
		if !strings.Contains(samples, guard) {
			t.Errorf("no sample is offered under %s", guard)
		}
	}
	// Both fences, and the CeTZ one with its import — the line people paste and
	// the line that used to fail. The harness compiles these exact snippets.
	for _, needed := range []string{"\"```plot\"", "\"```python plot\"", "@preview/cetz:"} {
		if !strings.Contains(samples, needed) {
			t.Errorf("the samples no longer contain %s", needed)
		}
	}

	// And every string it shows has a translation, since the app ships one.
	//
	// The whole sentence is compared, not a recognisable piece of it: these
	// strings are themselves the lookup keys, so a word changed anywhere in
	// one silently turns it into a key nothing translates. Checking a prefix
	// cannot see that — the mutation that proved it appended a letter to the
	// end of a key and every prefix assertion still passed.
	pt := string(repoFile(t, "internal/app/i18n_pt_br.go"))
	said := regexp.MustCompile(`ngT\("((?:[^"\\]|\\.)*)"\)`)
	found := map[string]bool{}
	for _, m := range said.FindAllStringSubmatch(views, -1) {
		found[m[1]] = true
	}
	for _, opening := range []string{
		"A plot block draws itself when the note opens",
		"A plot fence is drawn by the typesetter",
		"Examples copied from the web work as written",
		"Drawing needs a runtime this server does not have",
		"Reach for python plot when you need 3D",
	} {
		whole := ""
		for s := range found {
			if strings.HasPrefix(s, opening) {
				whole = s
				break
			}
		}
		if whole == "" {
			t.Errorf("the help no longer says anything starting %q", opening)
			continue
		}
		if !strings.Contains(pt, `"`+whole+`":`) {
			t.Errorf("no pt-BR translation for the exact string %q", whole)
		}
	}
}

// A caption is prose, and prose has formulas in it.
//
// It is the one piece of prose that never passes the math stage: formulas are
// lifted out of a note before Markdown is parsed, and a caption is a comment
// inside a fence, which that stage skips on purpose so the drawing's code
// reaches the engine as written. So "$\\vec{v}$" in a caption arrived as five
// literal characters in the figure and, after escaping, as literal dollars in
// the PDF.
func TestACaptionCanNameWhatItDescribes(t *testing.T) {
	run := readAsset(t, "static/run.js")
	if !strings.Contains(run, "ngCaptionInto(text, meta.caption)") {
		t.Error("a caption is written as plain text again; its math would show as dollars")
	}
	if !strings.Contains(run, "katex.render(") {
		t.Error("nothing typesets the caption's formulas")
	}
	// A caption is not worth losing to a typo inside it.
	if !strings.Contains(run, "throwOnError: false") {
		t.Error("a malformed formula would take the whole caption with it")
	}

	typst := readAsset(t, "static/typst.js")
	if !strings.Contains(typst, "function ngTypstCaption") {
		t.Fatal("the PDF has no caption-specific handling, so $…$ is escaped to dollars")
	}
	if n := strings.Count(typst, "ngTypstCaption(meta.caption"); n != 2 {
		t.Errorf("%d of 2 figure kinds typeset their caption's math", n)
	}
	// The same mitex call the prose uses, so a formula looks the same wherever
	// in the document it appears.
	if !strings.Contains(typst, `"#mi(" + ngTypstStr(part) + ")"`) {
		t.Error("a caption's math no longer goes through mitex like every other formula")
	}
}

// A label may be claimed once.
//
// Typst refuses a whole document in which one occurs twice — not the
// reference, the export — and a note earns that by being copied and pasted,
// which is a normal thing to do to a chapter. Losing the PDF entirely to that
// is out of proportion, so the first figure to claim a name keeps it.
//
// The note has to agree, or a reference points at one picture on screen and a
// different one in the document. It used to disagree: the note overwrote, so
// it resolved to the last copy while the PDF refused to build at all.
func TestADuplicatedLabelCostsTheLabelNotTheDocument(t *testing.T) {
	typst := readAsset(t, "static/typst.js")
	if !strings.Contains(typst, "claim: (name)") {
		t.Fatal("labels are no longer claimed, so a duplicate would fail the export")
	}
	if !strings.Contains(typst, "if (!label || ctx.claimed[label]) return \"\";") {
		t.Error("a repeated label is emitted again; Typst refuses the document")
	}
	if n := strings.Count(typst, "ctx.claim(meta.label)"); n != 2 {
		t.Errorf("%d of 2 figure kinds claim their label", n)
	}
	// And no path may still write a label directly.
	if strings.Contains(typst, `" <" + ngFigureLabel(meta.label) + ">"`) {
		t.Error("a figure still attaches its label without claiming it first")
	}

	run := readAsset(t, "static/run.js")
	if !strings.Contains(run, "hasOwnProperty.call(labels, label)") {
		t.Error("the note overwrites a duplicated label, so it would resolve to the " +
			"last copy while the PDF points at the first")
	}
}

// The examples in the README are compiled, not merely read.
//
// An example that no longer draws is a documentation bug of the worst kind:
// the reader types exactly what they were told and it fails, and nobody
// reviewing the prose would catch it. scripts/pipeline-harness.html runs every
// one of them through the real pipeline.
func TestTheDocumentedExamplesAreKeptRunnable(t *testing.T) {
	harness := string(repoFile(t, "scripts/pipeline-harness.html"))
	if !strings.Contains(harness, "readme-examples.json") {
		t.Error("the harness no longer compiles what the README publishes")
	}
	// Read from the README rather than copied into the harness, because a copy
	// drifts and then proves nothing about the published text.
	extractor := string(repoFile(t, "scripts/readme-examples.py"))
	if !strings.Contains(extractor, "## Drawing") {
		t.Error("the extractor no longer reads the Drawing section")
	}

	// The generated file has to match the README it was generated from, or the
	// harness checks a stale copy and reports success for text nobody ran.
	readme := string(repoFile(t, "README.md"))
	start := strings.Index(readme, "\n## Drawing\n")
	end := strings.Index(readme, "\n## How offline works\n")
	if start < 0 || end < 0 {
		t.Fatal("the Drawing section's headings moved; the extractor will fail too")
	}
	// (?m) so ^ and $ mean line boundaries, matching the extractor's re.M
	fences := regexp.MustCompile("(?sm)^```(plot|python plot)\n(.*?)^```$")
	want := len(fences.FindAllString(readme[start:end], -1))
	generated := string(repoFile(t, "scripts/readme-examples.json"))
	got := strings.Count(generated, `"lang":`)
	if want != got {
		t.Errorf("the README shows %d examples, readme-examples.json has %d; "+
			"run scripts/readme-examples.py", want, got)
	}

	// And the server it documents, since a plain file server silently breaks
	// every Python figure while everything else passes.
	server := string(repoFile(t, "scripts/harness-serve.py"))
	for _, needed := range []string{
		"Access-Control-Allow-Origin", "Cross-Origin-Resource-Policy", "ThreadingHTTPServer",
	} {
		if !strings.Contains(server, needed) {
			t.Errorf("the harness server no longer sets up %s; Pyodide will fail to fetch", needed)
		}
	}
	// The command itself, not a mention of the filename further down: the
	// instruction is the part that has to be right, and the first version of
	// this check passed while the instruction said the wrong thing.
	if !strings.Contains(harness, "python3 scripts/harness-serve.py") {
		t.Error("the harness still tells people to use a plain file server")
	}
}

// The landing page shows a real figure, and shows it the way the app does.
//
// It is the feature's only advertisement, so it has to be a drawing this
// typesetter actually produced rather than a picture of one — and it has to
// cost a visitor nothing, which rules out loading thirty megabytes of
// typesetter on a page whose job is to explain the app.
//
// Inlined rather than linked, because that is what lets it take the page's
// colour: an <img> is a document of its own and inherits nothing.
func TestTheLandingPageShowsARealFigure(t *testing.T) {
	svg := string(repoFile(t, "web/static/generated/landing-figure.svg"))
	if !strings.Contains(svg, "<svg") {
		t.Fatal("the landing figure is not an SVG")
	}
	// Typst writes its default colour as an explicit #000; the generator
	// rewrites exactly those so the axes follow the theme. Anything drawn in a
	// deliberate colour has to survive that, or the curve goes the same tone
	// as the axes it is drawn against.
	if !strings.Contains(svg, "currentColor") {
		t.Error("the figure carries a fixed colour; it cannot follow the theme")
	}
	if strings.Contains(svg, `"#000"`) {
		t.Error("some of the figure is still hard-black and will vanish in dark mode")
	}
	if !strings.Contains(svg, "#0074d9") {
		t.Error("the plotted curve lost its colour; it is now indistinguishable from the axes")
	}

	page := string(repoFile(t, "web/templates/landing.html"))
	if !strings.Contains(page, `{{template "landing-figure.svg"}}`) {
		t.Error("the figure is no longer inlined, so it cannot inherit the page's colour")
	}
	// Parsed alongside the page, or that template action fails at startup.
	server := string(repoFile(t, "internal/app/server.go"))
	if !strings.Contains(server, `"static/generated/landing-figure.svg"`) {
		t.Error("the figure is not parsed with the landing page; rendering it would panic")
	}

	css := string(repoFile(t, "web/static/style.css"))
	// Its own token rather than --text: a one-pixel line has far less of
	// itself to carry contrast than a letter does, so the ink goes to full
	// strength — pure white on dark, pure black on light.
	if !strings.Contains(css, ".shot-fig-plot { color: var(--figure-ink); }") {
		t.Error("the figure is no longer inked with the theme's figure colour")
	}
	for _, want := range []string{"--figure-ink: #000000;", "--figure-ink: #ffffff;"} {
		if !strings.Contains(css, want) {
			t.Errorf("%s is not defined; one theme has no ink", want)
		}
	}
	// Both dark paths: the explicit choice and the automatic one.
	if n := strings.Count(css, "--figure-ink: #ffffff;"); n != 2 {
		t.Errorf("dark ink is defined %d times; it needs both [data-theme=dark] "+
			"and the prefers-color-scheme block", n)
	}

	// And the generator that made it is kept, so it can be redrawn.
	gen := string(repoFile(t, "scripts/make-landing-figure.html"))
	if !strings.Contains(gen, "landing-figure.svg") {
		t.Error("the figure can no longer be regenerated")
	}
}
