package web

import (
	"net/url"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R2C-127: one mark, one meaning — and a leaf that says it is a leaf.
//
// The report came from two real URLs:
//
//	/golang/github.com/google/uuid?f_os=alpine+musl&f_version=v1.6.0#cube
//	…&f_symbol=whole+package&f_tool=go
//
// On the first the reader saw a triple-bar mark in the cells, read it as a
// hamburger — menu, more, go deeper — and clicked. The second is the bottom of
// the drill, and it said nothing about being the bottom, offered nothing to
// open, and wore the same mark. The mark had in fact been documented as BOTH
// "our own run passed here" and "there is code that works for this cell",
// which are different facts with different keys, so the misreading was
// earned.
//
// These tests hold the three separations that fixes it:
//
//	code availability   version + symbol, environment-blind
//	verification        this exact environment
//	drill-down          navigation, and nothing else, wears the chevron

// uuidNavStore is the reported coordinate as a store: one release measured on
// two OS buckets, package-level observations plus a symbol this network ran a
// contract against, and published samples — which exist for the RELEASE and
// the API and were all produced on Linux, as production's are.
func uuidNavStore() *fakeStore {
	f := newFakeStore()
	const (
		name = "github.com/google/uuid"
		v    = "v1.6.0"
		purl = "pkg:golang/" + name + "@" + v
	)
	f.versions["golang|"+name] = []string{v}
	f.symbols["golang|"+name+"|"+v] = []string{"uuid.New"}
	// Package level, two environments: the fleet's alpine container and a
	// developer machine on Windows. Same release, same API, different rows.
	f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "alpine musl", "x64",
		"go", "1.26", "go", "PROJECT_COMPILE", 4, 0)
	f.snapshots[snapKey(purl, "uuid.New")] = cubeSnap(purl, "uuid.New", "alpine musl", "x64",
		"go", "1.26", "go", "CONTRACT", 2, 0)
	// The published answers. Both were written and run on Linux; neither
	// stops existing when the reader pins Windows.
	f.sampleList = []SampleListItem{{
		SampleID: "sample-uuid-new", Goal: "generate a v4 UUID",
		Status: "PUBLISHED", Version: v, Symbols: []string{"uuid.New"},
	}}
	f.samplePackages["sample-uuid-new"] = []string{purl}
	// A second release with observations and no published answer, so the
	// unpinned page spreads release by API and the code mark has somewhere to
	// be absent: it belongs to v1.6.0's cells and to no others.
	const older = "pkg:golang/" + name + "@v1.5.0"
	f.versions["golang|"+name] = []string{v, "v1.5.0"}
	f.snapshots[snapKey(older, "")] = cubeSnap(older, "", "alpine musl", "x64",
		"go", "1.26", "go", "PROJECT_COMPILE", 2, 0)
	return f
}

const uuidPkg = "/golang/github.com/google/uuid"

// The exact second URL from the report.
const uuidLeaf = uuidPkg + "?f_os=alpine+musl&f_symbol=whole+package&f_tool=go&f_version=v1.6.0"

// A hamburger is a menu. Nothing on this site may say "code" or "basis" with
// three stacked bars again — the mark that caused the report is gone from the
// cell, the scan strip, the legend and every README that draws a grid.
func TestNoMarkOnTheSiteIsAHamburger(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	for _, path := range []string{uuidPkg, uuidLeaf, "/"} {
		body := get(t, mux, path).Body.String()
		if strings.Contains(body, "≡") {
			t.Errorf("%s still draws ≡, which readers take for a menu", path)
		}
	}
	if g := buildPivotCell(&pivotAgg{verPass: 1}, pivotNow).Glyph; g == "≡" {
		t.Error("buildPivotCell still produces the triple bar")
	}
}

// The two facts, apart. A cell says "there is code for this release and API"
// with a document, and "we ran it HERE" with the basis glyph, and neither
// borrows the other's mark.
func TestCodeAvailabilityAndEnvironmentVerificationAreDifferentMarks(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidLeaf+"&lang=ko").Body.String()

	card := answerCard(t, body)
	if !strings.Contains(card, "코드 있음") {
		t.Errorf("the leaf never says code exists for this release:\n%s", card)
	}
	// This coordinate is the package-level total, which developer machines
	// reported and the fleet never ran a contract against — so the honest
	// pair is "code exists" AND "not verified here", and the card has to be
	// able to say both at once. That pairing is the whole point: one mark
	// could only ever say one of them.
	if !strings.Contains(card, "이 환경 미검증") {
		t.Errorf("the leaf never says whether THIS environment was verified:\n%s", card)
	}
	if !strings.Contains(card, `class="state env`) {
		t.Errorf("the environment verdict has no mark of its own:\n%s", card)
	}
	if !strings.Contains(card, `class="codemark"`) {
		t.Errorf("code availability has no mark of its own:\n%s", card)
	}
}

// The contract the reporter asked for in as many words: a sample produced on
// Linux is still the code that exists for a Windows reader. Changing an
// environment filter may change the verification state and may not change
// code availability.
func TestAnEnvironmentFilterNeverDeletesCodeAvailability(t *testing.T) {
	code := newCodeIndex([]SampleListItem{{
		SampleID: "s1", Version: "v1.6.0", Symbols: []string{"uuid.New"},
	}})
	// The index takes a release and an API and nothing else. There is no
	// environment argument to pass, which is the guarantee: no caller can
	// make code availability depend on where it is being read.
	if n := code.at("v1.6.0", "uuid.New"); n != 1 {
		t.Fatalf("code at the release+API = %d, want 1", n)
	}
	if n := code.at("v1.6.0", "New"); n != 1 {
		t.Errorf("the axis spelling %q found no code; the site matches on the member", "New")
	}
	if n := code.at("v1.5.0", "uuid.New"); n != 0 {
		t.Errorf("code leaked across releases: v1.5.0 reports %d", n)
	}
	if n := code.at("v1.6.0", "uuid.Parse"); n != 0 {
		t.Errorf("code leaked across APIs: uuid.Parse reports %d", n)
	}
}

// And end to end, through the page: pin one OS, pin the other, read both.
func TestCodeStaysAndOnlyTheVerificationStateMovesAcrossEnvironments(t *testing.T) {
	f := uuidNavStore()
	// A second environment for the same release: a Windows machine reported
	// a build, and nothing of ours ever ran there.
	const purl = "pkg:golang/github.com/google/uuid@v1.6.0"
	f.snapshots[snapKey(purl, "uuid.New")] = twoEnvSnap(purl, "uuid.New")
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })

	base := uuidPkg + "?f_version=v1.6.0&f_symbol=" + url.QueryEscape("uuid.New") + "&lang=ko&f_os="
	for _, tc := range []struct{ os, wantEnv string }{
		{"alpine musl", "이 환경 검증됨"},
		{"windows", "이 환경 미검증"},
	} {
		body := get(t, mux, base+url.QueryEscape(tc.os)).Body.String()
		card := answerCard(t, body)
		if !strings.Contains(card, "코드 있음") {
			t.Errorf("os=%s: code availability vanished with the OS filter:\n%s", tc.os, card)
		}
		if !strings.Contains(card, tc.wantEnv) {
			t.Errorf("os=%s: want %q in the card:\n%s", tc.os, tc.wantEnv, card)
		}
	}
}

// The bottom of the drill says so. Before this the leaf and a landing looked
// identical, so the only way to learn there was nothing below was to click.
func TestTheLeafSaysItIsTheLeaf(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidLeaf+"&lang=ko").Body.String()

	if !strings.Contains(body, "최종 결과") {
		t.Errorf("the deepest coordinate never says it is the deepest:\n%s", truncate(body))
	}
	if !strings.Contains(body, `class="navterminal`) {
		t.Error("the navigator does not mark the coordinate terminal")
	}
	// And nothing on it offers a level that does not exist.
	if strings.Contains(cubeSection(t, body), `class="drill"`) {
		t.Error("a terminal coordinate still shows a drill-down affordance")
	}
}

// An evidence action exists because its destination does. The card used to
// print "samples and receipts for this coordinate" on every decided
// coordinate, including the ones with no sample to open.
func TestEvidenceActionsAppearOnlyWhereTheEvidenceDoes(t *testing.T) {
	withCode := uuidNavStore()
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = withCode })
	body := get(t, mux, uuidLeaf+"&lang=ko").Body.String()
	card := answerCard(t, body)
	if !strings.Contains(card, "샘플 코드") {
		t.Errorf("a coordinate with published code offers no way to it:\n%s", card)
	}
	if !strings.Contains(card, `class="answer-actions"`) {
		t.Errorf("the evidence actions are not their own grammar:\n%s", card)
	}

	// The same page with the samples taken away.
	bare := uuidNavStore()
	bare.sampleList, bare.samplePackages = nil, map[string][]string{}
	mux2, _ := newTestMux(t, func(d *Deps) { d.Store = bare })
	body2 := get(t, mux2, uuidLeaf+"&lang=ko").Body.String()
	card2 := answerCard(t, body2)
	if strings.Contains(card2, "샘플 코드") {
		t.Errorf("a coordinate with no published code still offers code:\n%s", card2)
	}
	if !strings.Contains(card2, "아직 코드/샘플 없음") {
		t.Errorf("the absence is not stated, it is merely missing:\n%s", card2)
	}
}

// Every clickable cell moves. A door that opens onto the room you are in is
// the dead end the report was about.
func TestNoClickableCellLeadsBackToTheSamePlace(t *testing.T) {
	for _, tc := range []struct{ name, path string }{
		{"uuid", uuidPkg},
		{"uuid pinned", uuidPkg + "?f_version=v1.6.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
			body := get(t, mux, tc.path).Body.String()
			here := canonicalPins(tc.path)
			for _, href := range regexpAllHrefs(cubeSection(t, body)) {
				if !strings.HasPrefix(href, uuidPkg+"?") && href != uuidPkg+"#cube" {
					continue
				}
				// The axis swap is a control, not a step: it redraws the same
				// slice the other way round and is meant to keep the pins.
				if strings.Contains(href, "x=") || strings.Contains(href, "y=") {
					continue
				}
				there := canonicalPins(href)
				if there == here {
					t.Errorf("a link inside the cube goes back to %s: %s", tc.path, href)
				}
			}
		})
	}
}

// In the grid too, and only where the grid decides the key. uuid unpinned
// spreads release by API: v1.6.0 has a published answer for uuid.New and
// v1.5.0 has none, so the document mark belongs to some cells and not others —
// which is what makes it readable at all.
func TestTheGridMarksCodeCellByCellAndNotEverywhere(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidPkg+"?lang=ko").Body.String()

	grid := cubeSection(t, body)
	i := strings.Index(grid, `<table class="pivot">`)
	if i < 0 {
		t.Fatalf("no grid to mark:\n%s", truncate(body))
	}
	table := grid[i : i+strings.Index(grid[i:], "</table>")]
	// Two: the release total and the one API that has a published answer.
	// v1.5.0's cells carry none, which is what makes the mark readable — a
	// mark on every cell says nothing.
	if n := strings.Count(table, `class="codemark"`); n != 2 {
		t.Errorf("the grid carries %d code marks; v1.6.0 has a published answer and v1.5.0 has none:\n%s",
			n, table)
	}
	for _, row := range strings.Split(table, "<td ")[1:] {
		cell := row
		if j := strings.Index(cell, "</td>"); j >= 0 {
			cell = cell[:j]
		}
		if strings.Contains(cell, "f_version=v1.5.0") && strings.Contains(cell, "codemark") {
			t.Errorf("a release with no published answer is marked as having code:\n%s", cell)
		}
		// The chevron is on the doors and nowhere else, so "there is code
		// here" and "there is a level below here" can never be read off one
		// mark again — and a cell with nothing behind it wears neither.
		door := strings.Contains(cell, "<a href=")
		if door != strings.Contains(cell, `class="drill"`) {
			t.Errorf("cell is a door=%v but its chevron disagrees:\n%s", door, cell)
		}
	}
}

// "Terminal" means DECIDED, not "the grid gave up". uuid pinned to its one
// release spreads over two symbol values that the axes cannot pair, so no
// grid is built and the page takes the leaf arm — with the symbol rung still
// open under it. Announcing the bottom of the drill there is the same lie in
// the other direction: the reader is told to stop where two records are still
// waiting to be told apart.
func TestAnUndecidedSliceIsNotAnnouncedAsTheBottom(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidPkg+"?f_version=v1.6.0&lang=ko").Body.String()

	if strings.Contains(body, `class="navterminal`) {
		t.Errorf("a slice with an open symbol rung claims to be terminal:\n%s", truncate(body))
	}
	if !strings.Contains(body, `class="navnext`) {
		t.Error("the navigator names no next step on a slice that has one")
	}
	// And the records under it are doors, since the grid could draw none.
	if !strings.Contains(body, `class="leafpin`) {
		t.Errorf("the exact records offer no way down, and the grid drew none either:\n%s",
			truncate(body))
	}
	if !strings.Contains(body, "f_symbol=uuid.New") {
		t.Error("the way down does not pin the symbol that tells the records apart")
	}
	// An undecided slice covers several APIs. It is not the package-level
	// total, and saying so states a coordinate the reader has not reached.
	if strings.Contains(body, "패키지 전체 집계") {
		t.Error("an undecided slice is labelled as the package-level total")
	}
}

// "whole package" is a package-level total, not an API. Renaming it to the
// package's own name stopped it reading as generic and started it reading as
// an export.
func TestThePackageLevelTotalIsNotDressedAsAnAPI(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	card := answerCard(t, get(t, mux, uuidLeaf+"&lang=ko").Body.String())
	if !strings.Contains(card, "패키지 전체 집계") {
		t.Errorf("the package-level total is not named as one:\n%s", card)
	}
}

// The navigator names the rungs, marks the one the reader is on, and its way
// back up keeps the language and the pins above it.
func TestTheNavigatorNamesTheDepthAndStepsBackCleanly(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = uuidNavStore() })
	body := get(t, mux, uuidLeaf+"&lang=ko").Body.String()

	i := strings.Index(body, `class="cubenav`)
	if i < 0 {
		t.Fatal("the instrument has no navigator")
	}
	nav := body[i : i+strings.Index(body[i:], "</nav>")]
	for _, want := range []string{"패키지", "버전", "환경 / 도구", "심볼 / API", "샘플 / 증거"} {
		if !strings.Contains(nav, want) {
			t.Errorf("the navigator omits the %q rung:\n%s", want, nav)
		}
	}
	if !strings.Contains(nav, `aria-current="step"`) {
		t.Errorf("nothing in the navigator says where the reader is:\n%s", nav)
	}
	for _, href := range regexpAllHrefs(nav) {
		if !strings.Contains(href, "lang=ko") {
			t.Errorf("a step back drops the page language: %s", href)
		}
		if strings.Contains(href, "f_symbol=") && strings.Contains(href, "f_version=") {
			// Stepping back to the version rung must drop what is below it.
			t.Errorf("a step back kept the pins it was meant to undo: %s", href)
		}
	}
}

// The same contract in three ecosystems, because none of it is about Go.
func TestTheNavigatorContractHoldsInEveryEcosystem(t *testing.T) {
	for _, eco := range []struct{ eco, name, symbol string }{
		{"npm", "semverish", "semver.clean"},
		{"golang", "example.com/mod", "Mod.New"},
		{"pypi", "flasky", "flasky.Flask"},
	} {
		t.Run(eco.eco, func(t *testing.T) {
			f := newFakeStore()
			purl := "pkg:" + eco.eco + "/" + eco.name + "@1.2.3"
			f.versions[eco.eco+"|"+eco.name] = []string{"1.2.3"}
			f.symbols[eco.eco+"|"+eco.name+"|1.2.3"] = []string{eco.symbol}
			f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "linux", "x64",
				"node", "22", "npm", "PROJECT_COMPILE", 5, 0)
			f.snapshots[snapKey(purl, eco.symbol)] = cubeSnap(purl, eco.symbol, "linux", "x64",
				"node", "22", "npm", "CONTRACT", 1, 0)
			f.sampleList = []SampleListItem{{
				SampleID: "s", Version: "1.2.3", Symbols: []string{eco.symbol},
			}}
			f.samplePackages["s"] = []string{purl}
			mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })

			leaf := pkgHref(eco.eco, eco.name) + "?f_version=1.2.3&f_symbol=" +
				url.QueryEscape(eco.symbol) + "&lang=ko"
			body := get(t, mux, leaf).Body.String()
			if strings.Contains(body, "≡") {
				t.Error("the hamburger survived in this ecosystem")
			}
			if !strings.Contains(body, `class="cubenav`) {
				t.Error("no navigator")
			}
			card := answerCard(t, body)
			for _, want := range []string{"코드 있음", "최종 결과", "샘플 코드"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing from the leaf:\n%s", want, card)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------

// answerCard returns the answer card's markup, failing when there is none.
func answerCard(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `class="answer `)
	if i < 0 {
		t.Fatalf("no answer card on the page:\n%s", truncate(body))
	}
	rest := body[i:]
	j := strings.Index(rest, `<details id="cubechange"`)
	if j < 0 {
		j = len(rest)
	}
	return rest[:j]
}

// cubeSection returns everything inside #cube.
func cubeSection(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `id="cube"`)
	if i < 0 {
		t.Fatal("no cube on the page")
	}
	sec := body[i:]
	if j := strings.Index(sec, "</section>"); j >= 0 {
		sec = sec[:j]
	}
	return sec
}

// canonicalPins reduces a cube URL to the pins it sets, which is the state
// the page renders from. Two URLs with the same pins are the same place.
func canonicalPins(raw string) string {
	raw = strings.TrimSuffix(raw, "#cube")
	u, err := url.Parse(strings.ReplaceAll(raw, "&amp;", "&"))
	if err != nil {
		return raw
	}
	pins := url.Values{}
	for _, dim := range cubeDimKeys {
		if v := u.Query().Get("f_" + dim); v != "" {
			pins.Set("f_"+dim, v)
		}
	}
	return u.Path + "?" + pins.Encode()
}

// twoEnvSnap is one symbol measured in two environment buckets: a contract
// this network ran on alpine, and a build a Windows machine reported.
func twoEnvSnap(purl, symbol string) string {
	return `{
	  "schemaVersion": 1,
	  "purl": "` + purl + `",
	  "symbol": "` + symbol + `",
	  "generatedAt": "2026-08-13T00:00:00Z",
	  "rows": [{
	    "envBucket": {"schemaVersion":1,"os":"alpine","libc":"musl","arch":"x64",
	      "runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
	    "confidence": "MEDIUM",
	    "passRate": 1,
	    "lastSeen": "2026-08-12T10:00:00Z",
	    "byStage": {"CONTRACT": {"pass": 2, "fail": 0}}
	  },{
	    "envBucket": {"schemaVersion":1,"os":"windows","osVersion":"11","arch":"x64",
	      "runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
	    "confidence": "MEDIUM",
	    "passRate": 1,
	    "lastSeen": "2026-08-12T10:00:00Z",
	    "byStage": {"PROJECT_COMPILE": {"pass": 3, "fail": 0}}
	  }],
	  "failures": []
	}`
}
