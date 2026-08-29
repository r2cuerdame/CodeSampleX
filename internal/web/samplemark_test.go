package web

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R2C-127, reopened: one document, and its colour is how the sample ran.
//
// The first implementation split one mark into two — a document for "there is
// code for this release and API" and a diamond for "we ran a contract in THIS
// environment" — and stated both side by side. That is two internal models
// (Sample, Evidence) drawn as two glyphs, and in real use it did not read: a
// reader standing on a coordinate wants one fact, and the fact they want is
//
//	a document means there is a sample here, and its colour is how it ran here.
//
// So the diamond is gone as a user-facing verification mark and the document
// carries the state:
//
//	no document      no sample at this coordinate
//	grey document    a sample, with no run of ours recorded here
//	green document   a sample, and our run of it passed here
//	red document     a sample, and our run of it failed here
//	split document   a sample, and both outcomes are recorded here
//
// Sample EXISTENCE is still keyed by release and API and still takes no
// environment argument (cubecode.go), so an OS filter can move the colour and
// can never delete the document. That separation is what the first
// implementation got right and it is kept here unchanged.

const marksPkg = "/golang/example.com/marks"

// markRelease is the release every published answer in the fixture belongs
// to. markOldRelease has observations and no published answer at all.
const (
	markRelease    = "v1.0.0"
	markOldRelease = "v2.0.0"
)

// The slice that spreads API down and RELEASE across: the one grid where
// every state is visible at once.
//
// Deliberately not an environment axis. A grid spread over WHERE things ran
// RATES its cells from observations only (observationsOnlyOnEnvironmentAxes)
// and picks its axes over that rate evidence, so an OS axis would fall back
// here — our runs would keep colouring the documents, but they give an
// environment axis no measured spread. Release × API is the pair that renders
// our runs in full, which is the pair the document's colour is about.
const marksGrid = marksPkg + "?x=version&y=symbol&lang=ko"

// sampleMarkStore holds one release whose four APIs cover every state, plus a
// second release nobody published an answer for.
func sampleMarkStore() *fakeStore {
	f := newFakeStore()
	const (
		name = "example.com/marks"
		v    = "v1.0.0"
		purl = "pkg:golang/" + name + "@" + v
		old  = "pkg:golang/" + name + "@v2.0.0"
	)
	f.versions["golang|"+name] = []string{"v2.0.0", v}
	f.symbols["golang|"+name+"|"+v] = []string{"marks.Pass", "marks.Fail", "marks.Both", "marks.Quiet"}

	// Our own contract, run on linux: clean, failed, and both at once.
	f.snapshots[snapKey(purl, "marks.Pass")] = cubeSnap(purl, "marks.Pass",
		"linux", "x64", "go", "1.26", "go", "CONTRACT", 2, 0)
	f.snapshots[snapKey(purl, "marks.Fail")] = cubeSnap(purl, "marks.Fail",
		"linux", "x64", "go", "1.26", "go", "CONTRACT", 0, 2)
	f.snapshots[snapKey(purl, "marks.Both")] = cubeSnap(purl, "marks.Both",
		"linux", "x64", "go", "1.26", "go", "CONTRACT", 1, 1)
	// A published answer our fleet never ran at this coordinate: real
	// projects reported builds and nothing of ours executed. That is the grey
	// document, and it is a different answer from "no sample".
	f.snapshots[snapKey(purl, "marks.Quiet")] = cubeSnap(purl, "marks.Quiet",
		"linux", "x64", "go", "1.26", "go", "PROJECT_COMPILE", 3, 0)
	// A release with observations and no published answer at all: no
	// document, because there is no code here to promise.
	f.snapshots[snapKey(old, "")] = cubeSnap(old, "",
		"linux", "x64", "go", "1.26", "go", "PROJECT_COMPILE", 1, 0)

	f.sampleList = []SampleListItem{
		{SampleID: "s-pass", Goal: "pass", Status: "PUBLISHED", Version: v, Symbols: []string{"marks.Pass"}},
		{SampleID: "s-fail", Goal: "fail", Status: "PUBLISHED", Version: v, Symbols: []string{"marks.Fail"}},
		{SampleID: "s-both", Goal: "both", Status: "PUBLISHED", Version: v, Symbols: []string{"marks.Both"}},
		{SampleID: "s-quiet", Goal: "quiet", Status: "PUBLISHED", Version: v, Symbols: []string{"marks.Quiet"}},
	}
	f.samplePackages = map[string][]string{
		"s-pass": {purl}, "s-fail": {purl}, "s-both": {purl}, "s-quiet": {purl},
	}
	return f
}

// markRe pulls every document mark out of a fragment, in document order, with
// the state it is drawn in and the wording it carries.
var markRe = regexp.MustCompile(`class="smark s-([a-z]+)"[^>]*aria-label="([^"]*)"[^>]*title="([^"]*)"`)

type renderedMark struct{ state, aria, title string }

func marksIn(fragment string) []renderedMark {
	var out []renderedMark
	for _, m := range markRe.FindAllStringSubmatch(fragment, -1) {
		out = append(out, renderedMark{m[1], html.UnescapeString(m[2]), html.UnescapeString(m[3])})
	}
	return out
}

func gridTable(t *testing.T, body string) string {
	t.Helper()
	sec := cubeSection(t, body)
	i := strings.Index(sec, `<table class="pivot">`)
	if i < 0 {
		t.Fatalf("no grid on the page:\n%s", truncate(body))
	}
	return sec[i : i+strings.Index(sec[i:], "</table>")]
}

func markGridCells(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, chunk := range strings.Split(gridTable(t, body), "<td ")[1:] {
		if j := strings.Index(chunk, "</td>"); j >= 0 {
			chunk = chunk[:j]
		}
		out = append(out, chunk)
	}
	return out
}

// cellFor returns the one grid cell whose link pins these two axis values.
func cellFor(t *testing.T, body, symbol, version string) string {
	t.Helper()
	for _, cell := range markGridCells(t, body) {
		if strings.Contains(cell, "f_symbol="+url.QueryEscape(symbol)) &&
			strings.Contains(cell, "f_version="+url.QueryEscape(version)) {
			return cell
		}
	}
	t.Fatalf("no cell for %s x %s in:\n%s", symbol, version, gridTable(t, body))
	return ""
}

// rowFor returns one whole row of the grid, header included.
func rowFor(t *testing.T, body, label string) string {
	t.Helper()
	for _, row := range strings.Split(gridTable(t, body), "<tr")[1:] {
		if j := strings.Index(row, "</tr>"); j >= 0 {
			row = row[:j]
		}
		if strings.Contains(row, ">"+label+"<") {
			return row
		}
	}
	t.Fatalf("no row labelled %q in:\n%s", label, gridTable(t, body))
	return ""
}

// sampleSurfaces returns every part of a page that speaks about samples: the
// grids, the legends that teach their marks, the answer card and the exact
// records. It exists so a rule about the sample vocabulary can be enforced
// where that vocabulary is spoken, and nowhere else — the same characters do
// honest work in prose elsewhere on the site, and banning them page-wide
// would be a rule about the alphabet rather than about the marks.
func sampleSurfaces(body string) []string {
	var out []string
	for _, open := range []struct{ start, end string }{
		{`<table class="pivot">`, "</table>"},
		{`class="pivotlegend-marks`, "</ul>"},
		{`class="gridstats`, "</div>"},
		{`class="answer `, "</div>"},
		{`class="cubeleaf`, "</ul>"},
	} {
		rest := body
		for {
			i := strings.Index(rest, open.start)
			if i < 0 {
				break
			}
			rest = rest[i:]
			j := strings.Index(rest, open.end)
			if j < 0 {
				out = append(out, rest)
				break
			}
			out = append(out, rest[:j])
			rest = rest[j+len(open.end):]
		}
	}
	return out
}

func legendBlock(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `class="pivotlegend-marks`)
	if i < 0 {
		t.Fatalf("no legend on the page:\n%s", truncate(body))
	}
	rest := body[i:]
	return rest[:strings.Index(rest, "</ul>")]
}

// ---------------------------------------------------------------------------
// 1. No sample at the coordinate — no document, and nothing that looks like an
//    affordance onto code that is not there.

func TestNoSampleDrawsNoDocument(t *testing.T) {
	if got := deriveSampleState(0, 0, 0); got != sampleNone {
		t.Errorf("nothing published and nothing run = %q, want no mark", got)
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	// v2.0.0 has observations and no published answer.
	body := get(t, mux, marksPkg+"?x=os&y=version&lang=ko").Body.String()
	row := rowFor(t, body, "v2.0.0")
	if got := marksIn(row); len(got) != 0 {
		t.Errorf("a release with no published sample is marked %v:\n%s", got, row)
	}
}

// ---------------------------------------------------------------------------
// 2. A sample exists and nothing of ours ran here — grey, and said in words.
//    "No sample" and "a sample nobody has run here" are different answers, and
//    the page has to be able to tell them apart.

func TestASampleNobodyRanHereIsGrey(t *testing.T) {
	if got := deriveSampleState(1, 0, 0); got != sampleUnknown {
		t.Errorf("published, never run here = %q, want %q", got, sampleUnknown)
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	body := get(t, mux, marksGrid).Body.String()
	cell := cellFor(t, body, "marks.Quiet", markRelease)
	got := marksIn(cell)
	if len(got) != 1 || got[0].state != string(sampleUnknown) {
		t.Fatalf("a published sample with no run of ours here is drawn %v:\n%s", got, cell)
	}
	if !strings.Contains(got[0].aria, "샘플") {
		t.Errorf("the grey document never says a sample is there: %q", got[0].aria)
	}
}

// ---------------------------------------------------------------------------
// 3, 4, 5. PASS, FAIL, and both at once.

func TestTheDocumentTakesItsColourFromHowTheSampleRanHere(t *testing.T) {
	for _, tc := range []struct {
		name             string
		pass, fail       int64
		want             sampleState
		symbol, wantWord string
	}{
		{"passed", 2, 0, samplePass, "marks.Pass", "통과"},
		{"failed", 0, 2, sampleFail, "marks.Fail", "실패"},
		{"both", 1, 1, sampleMixed, "marks.Both", "통과와 실패"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveSampleState(1, tc.pass, tc.fail); got != tc.want {
				t.Errorf("pass=%d fail=%d = %q, want %q", tc.pass, tc.fail, got, tc.want)
			}
			// A run recorded at this coordinate is itself proof a sample was
			// here, so the state does not wait for the published aggregate to
			// agree before it says what happened.
			if got := deriveSampleState(0, tc.pass, tc.fail); got != tc.want {
				t.Errorf("a recorded run with no published aggregate = %q, want %q", got, tc.want)
			}
			mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
			body := get(t, mux, marksGrid).Body.String()
			cell := cellFor(t, body, tc.symbol, markRelease)
			got := marksIn(cell)
			if len(got) != 1 || got[0].state != string(tc.want) {
				t.Fatalf("%s is drawn %v, want one %q document:\n%s", tc.symbol, got, tc.want, cell)
			}
			if !strings.Contains(got[0].aria, tc.wantWord) {
				t.Errorf("the %s document never says %q: %q", tc.want, tc.wantWord, got[0].aria)
			}
		})
	}
}

// A split document must not lose either fact to a single colour. Both halves
// are drawn, so "some passed and some failed" cannot be read as either one.
func TestAMixedDocumentKeepsBothOutcomes(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	body := get(t, mux, marksGrid).Body.String()
	cell := cellFor(t, body, "marks.Both", markRelease)
	if !strings.Contains(cell, `class="smark s-mixed"`) {
		t.Fatalf("a coordinate with a pass and a fail is not drawn as mixed:\n%s", cell)
	}
	// The two halves are inside the icon, not a second glyph beside it.
	if !strings.Contains(cell, `class="s-half pass"`) || !strings.Contains(cell, `class="s-half fail"`) {
		t.Errorf("the mixed document is not split into a passing and a failing half:\n%s", cell)
	}
	if n := len(marksIn(cell)); n != 1 {
		t.Errorf("a mixed coordinate carries %d marks; the contract is one document", n)
	}
}

// ---------------------------------------------------------------------------
// 6. The eye, the mouse and the screen reader are told the same thing, and the
//    legend teaches that same sentence.

func TestTheDocumentSaysOneThingToEveryReader(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	body := get(t, mux, marksGrid).Body.String()
	all := marksIn(cubeSection(t, body))
	if len(all) == 0 {
		t.Fatal("no document marks on a page built to be full of them")
	}
	seen := map[string]bool{}
	for _, m := range all {
		if m.aria == "" {
			t.Errorf("a %s document has no accessible name", m.state)
		}
		if m.aria != m.title {
			t.Errorf("%s document: hover says %q, screen reader says %q", m.state, m.title, m.aria)
		}
		seen[m.state] = true
	}
	// And the legend teaches exactly those sentences, so the wording a reader
	// learns under the grid is the wording the cell hands them.
	legend := legendBlock(t, body)
	for state := range seen {
		want := sampleStateLabel("ko", sampleState(state))
		if !strings.Contains(legend, html.EscapeString(want)) {
			t.Errorf("the legend never teaches the %s document (%q):\n%s", state, want, legend)
		}
	}
	// Including the state that has no icon: "there is nothing here" is an
	// answer, and a legend that only lists marks cannot give it.
	if none := sampleStateLabel("ko", sampleNone); !strings.Contains(legend, html.EscapeString(none)) {
		t.Errorf("the legend never says what NO document means (%q):\n%s", none, legend)
	}
}

// ---------------------------------------------------------------------------
// 7. Sample state and navigation are different grammars. The document never
//    means "go deeper", and a cell that is a door says so with the chevron.

func TestTheDocumentAndTheWayDownAreIndependent(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	body := get(t, mux, marksGrid).Body.String()

	var doors, marked, both int
	for _, cell := range markGridCells(t, body) {
		door := strings.Contains(cell, "<a href=")
		mark := len(marksIn(cell)) > 0
		if door != strings.Contains(cell, `class="drill"`) {
			t.Errorf("cell is a door=%v but its chevron disagrees:\n%s", door, cell)
		}
		if door {
			doors++
		}
		if mark {
			marked++
		}
		if door && mark {
			both++
		}
	}
	if doors == 0 || marked == 0 {
		t.Fatalf("the fixture proves nothing: %d doors, %d marks", doors, marked)
	}
	// Independent means the two sets are not one set: there is at least one
	// door with no sample, or one sample behind no door.
	if both == doors && both == marked {
		t.Error("every door carries a document and every document is a door; the two marks are not separable")
	}
}

// ---------------------------------------------------------------------------
// 9. The same rule at every depth: the package-level total, one API, and one
//    exact environment all answer with a document and never with a diamond.

func TestTheSameRuleHoldsAtEveryCoordinate(t *testing.T) {
	f := sampleMarkStore()
	// Package-level evidence for the release our fleet ran: the aggregate row
	// is a coordinate too.
	const purl = "pkg:golang/example.com/marks@v1.0.0"
	f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "linux", "x64",
		"go", "1.26", "go", "CONTRACT", 2, 0)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })

	for _, tc := range []struct{ name, path, want string }{
		{"package level", marksPkg + "?f_version=v1.0.0&f_symbol=" +
			url.QueryEscape(cubePackageLevel) + "&f_os=linux&lang=ko", "pass"},
		{"one API", marksPkg + "?f_version=v1.0.0&f_symbol=" +
			url.QueryEscape("marks.Fail") + "&f_os=linux&lang=ko", "fail"},
		{"exact environment", marksPkg + "?f_version=v1.0.0&f_symbol=" +
			url.QueryEscape("marks.Both") + "&f_os=linux&f_tool=go&lang=ko", "mixed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := get(t, mux, tc.path).Body.String()
			card := answerCard(t, body)
			got := marksIn(card)
			if len(got) != 1 || got[0].state != tc.want {
				t.Fatalf("the card draws %v, want one %q document:\n%s", got, tc.want, card)
			}
			// One state chip, not a pair. The old card said "code available"
			// and "not verified in this environment" side by side, which is
			// the two-model split this issue removed.
			if n := strings.Count(card, `class="state `); n != 1 {
				t.Errorf("the card carries %d state chips; the contract is one:\n%s", n, card)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The separation the first implementation got right, restated on the new mark:
// an environment filter moves the colour and never deletes the document.

func TestAnEnvironmentFilterMovesTheColourAndKeepsTheDocument(t *testing.T) {
	f := sampleMarkStore()
	const purl = "pkg:golang/example.com/marks@v1.0.0"
	// The same API measured twice: our contract on alpine, a reported build
	// on windows. The sample exists in both places; only one was run.
	f.snapshots[snapKey(purl, "marks.Pass")] = twoEnvSnap(purl, "marks.Pass")
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })

	// The OS dimension is the label the environment was RECORDED under, with
	// libc attached where one was recorded: "alpine musl", not "alpine". The
	// Windows row recorded no libc, so its label is the bare platform name —
	// which is also how a family pin is spelled, and matches the same row
	// either way (dimValueMatches).
	base := marksPkg + "?f_version=v1.0.0&f_symbol=" + url.QueryEscape("marks.Pass") + "&lang=ko&f_os="
	for _, tc := range []struct{ os, want string }{
		{"alpine musl", "pass"},
		{"windows", "unknown"},
	} {
		card := answerCard(t, get(t, mux, base+url.QueryEscape(tc.os)).Body.String())
		got := marksIn(card)
		if len(got) != 1 {
			t.Fatalf("os=%s: the document vanished with the OS filter:\n%s", tc.os, card)
		}
		if got[0].state != tc.want {
			t.Errorf("os=%s: document is %q, want %q", tc.os, got[0].state, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// 10. The diamond is gone as a user-facing mark. It said "verified here",
//     which is the colour of the document now, and leaving it on the page
//     would put the two-glyph reading straight back.

func TestNoDiamondSurvivesAsAUserFacingMark(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = sampleMarkStore() })
	for _, path := range []string{"/", marksPkg, marksGrid, marksPkg + "?f_version=v1.0.0&lang=ko"} {
		body := get(t, mux, path).Body.String()
		// Scoped to the surfaces that speak about samples. The same
		// characters are legitimate elsewhere on the site — the landing's
		// "this never leaves your machine" list is a column of crosses, and
		// nothing there claims to be a cell.
		for _, surface := range sampleSurfaces(body) {
			for _, glyph := range []string{"◆", "✕", "≡"} {
				if strings.Contains(surface, glyph) {
					t.Errorf("%s still draws %q where it speaks about samples:\n%s",
						path, glyph, surface)
				}
			}
		}
	}
	if g := buildPivotCell(&pivotAgg{verPass: 1}, pivotNow).Glyph; g != "" {
		t.Errorf("a verified cell still carries the glyph %q; its state is the document now", g)
	}
	if g := buildPivotCell(&pivotAgg{verFail: 1}, pivotNow).Glyph; g != "" {
		t.Errorf("a failed cell still carries the glyph %q; its state is the document now", g)
	}
}

// ---------------------------------------------------------------------------
// 8. A phone. The marks have to be legible at 320px, they have to stay inside
//    the page, and the states must be different colours to the eye as well as
//    different sentences to a reader.

type markBox struct {
	State string `json:"state"`
	// Decorative marks are the legend's swatches: the sentence beside them
	// names the state, so repeating it on the icon would make a screen
	// reader say it twice. They still have to be the right COLOUR, which is
	// the whole reason the legend teaches anything.
	Decorative bool    `json:"decorative"`
	Colour     string  `json:"colour"`
	Left       float64 `json:"left"`
	Right      float64 `json:"right"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Aria       string  `json:"aria"`
}

type markReport struct {
	Width  int       `json:"width"`
	Client float64   `json:"clientWidth"`
	Scroll float64   `json:"scrollWidth"`
	Marks  []markBox `json:"marks"`
	Legend int       `json:"legend"`
}

var markViewports = []int{320, 360, 390, 430}

func TestTheSampleMarksSurviveAPhone(t *testing.T) {
	chrome := findChrome(t)
	// Both surfaces a phone reader meets a mark on: the grid, where several
	// states sit side by side and have to be distinguishable, and the exact
	// coordinate reported from production, where there is one mark and one
	// card and neither may push the page sideways.
	for _, page := range []struct {
		name       string
		store      func() *fakeStore
		target     string
		wantStates int
		wantLegend bool
	}{
		{"grid", sampleMarkStore, marksGrid, 3, true},
		{"reported leaf", pgxStore, pgxLeaf, 1, false},
	} {
		t.Run(page.name, func(t *testing.T) {
			measureMarks(t, chrome, page.store(), page.target, page.wantStates, page.wantLegend)
		})
	}
}

func measureMarks(t *testing.T, chrome string, store *fakeStore, target string,
	wantStates int, wantLegend bool) {

	t.Helper()
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })
	srv := httptest.NewServer(markHarness(mux, target))
	defer srv.Close()

	var reports []markReport
	payload := renderMeasurement(t, chrome, srv.URL+markMeasurePath)
	if err := json.Unmarshal([]byte(payload), &reports); err != nil {
		t.Fatalf("measurement %q: %v", payload, err)
	}
	if len(reports) != len(markViewports) {
		t.Fatalf("measured %d viewports, want %d", len(reports), len(markViewports))
	}

	for _, r := range reports {
		// body carries overflow-x:hidden, which hides the scrollbar without
		// shrinking scrollWidth — so this reads the number, not the bar.
		if over := r.Scroll - r.Client; over > 0 {
			t.Errorf("viewport %dpx: documentElement.scrollWidth %.0f > clientWidth %.0f (%.0fpx of horizontal overflow)",
				r.Width, r.Scroll, r.Client, over)
		}
		if len(r.Marks) == 0 {
			t.Errorf("viewport %dpx: no document mark rendered at all", r.Width)
			continue
		}
		if wantLegend && r.Legend == 0 {
			t.Errorf("viewport %dpx: the legend that names the states did not render", r.Width)
		}
		colours := map[string]string{}
		for _, m := range r.Marks {
			if m.Width < 8 || m.Height < 8 {
				t.Errorf("viewport %dpx: a %s document is %.0fx%.0f — too small to read",
					r.Width, m.State, m.Width, m.Height)
			}
			if m.Left < 0 || m.Right > r.Client+0.5 {
				t.Errorf("viewport %dpx: a %s document sits at [%.0f, %.0f], outside [0, %.0f]",
					r.Width, m.State, m.Left, m.Right, r.Client)
			}
			if !m.Decorative && strings.TrimSpace(m.Aria) == "" {
				t.Errorf("viewport %dpx: a %s document has no accessible name", r.Width, m.State)
			}
			if prev, ok := colours[m.State]; ok && prev != m.Colour {
				t.Errorf("viewport %dpx: two %s documents are drawn %s and %s",
					r.Width, m.State, prev, m.Colour)
			}
			colours[m.State] = m.Colour
		}
		// Colour is the whole point of the mark, so no two states may render
		// the same one.
		byColour := map[string]string{}
		for state, c := range colours {
			if other, clash := byColour[c]; clash {
				t.Errorf("viewport %dpx: %s and %s are both drawn %s", r.Width, other, state, c)
			}
			byColour[c] = state
		}
		if len(colours) < wantStates {
			t.Errorf("viewport %dpx: %d states rendered, want at least %d",
				r.Width, len(colours), wantStates)
		}
	}
}

const markMeasurePath = "/__sample-mark-measure"

func markHarness(mux *http.ServeMux, target string) http.Handler {
	widths := make([]string, 0, len(markViewports))
	frames := make([]string, 0, len(markViewports))
	for _, w := range markViewports {
		widths = append(widths, fmt.Sprint(w))
		frames = append(frames, fmt.Sprintf(
			`<iframe id="f%d" width="%d" height="1400" src="%s"></iframe>`, w, w, html.EscapeString(target)))
	}
	page := strings.NewReplacer(
		"__WIDTHS__", strings.Join(widths, ","),
		"__FRAMES__", strings.Join(frames, "\n"),
	).Replace(markHarnessHTML)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != markMeasurePath {
			mux.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
}

// The legend lives inside a <details>; it is opened before measuring, because
// a legend a reader can open is a legend that has to fit.
const markHarnessHTML = `<!doctype html>
<meta charset="utf-8"><title>sample mark measure</title>
<body style="margin:0">
__FRAMES__
<pre id="measure">PENDING</pre>
<script>
function run(){
  var out = [], widths = [__WIDTHS__];
  for (var i = 0; i < widths.length; i++) {
    var w = widths[i], fr = document.getElementById('f' + w);
    var doc = fr.contentDocument, win = fr.contentWindow, de = doc.documentElement;
    var ds = doc.querySelectorAll('details');
    for (var d = 0; d < ds.length; d++) ds[d].open = true;
    var marks = [], els = doc.querySelectorAll('.smark');
    for (var j = 0; j < els.length; j++) {
      var el = els[j], r = el.getBoundingClientRect(), cs = win.getComputedStyle(el);
      var cls = (el.getAttribute('class') || '').match(/s-([a-z]+)/);
      marks.push({state: cls ? cls[1] : '', colour: cs.color,
        decorative: el.getAttribute('aria-hidden') === 'true',
        left: Math.round(r.left + win.scrollX), right: Math.round(r.right + win.scrollX),
        width: Math.round(r.width), height: Math.round(r.height),
        aria: el.getAttribute('aria-label') || ''});
    }
    out.push({width: w, clientWidth: de.clientWidth, scrollWidth: de.scrollWidth,
      marks: marks, legend: doc.querySelectorAll('.pivotlegend-marks li').length});
  }
  document.getElementById('measure').textContent = JSON.stringify(out);
}
window.addEventListener('load', function(){ setTimeout(run, 500); });
</script>
`
