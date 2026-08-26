package web

import (
	"regexp"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// The package page answers first and explores second.

// answerStore is one package measured down to a single coordinate: an exact
// link lands on it with nothing left to choose.
func answerStore() *fakeStore {
	f := newFakeStore()
	const v = "6.3.1"
	purl := "pkg:npm/semverish@" + v
	f.versions["npm|semverish"] = []string{v}
	f.symbols["npm|semverish|"+v] = []string{"semver.clean"}
	f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "ubuntu", "x64",
		"node", "22", "npm", "PROJECT_COMPILE", 3, 0)
	f.snapshots[snapKey(purl, "semver.clean")] = cubeSnap(purl, "semver.clean", "ubuntu", "x64",
		"node", "22", "npm", "CONTRACT", 1, 0)
	return f
}

const exactLink = "/npm/semverish?f_symbol=semver.clean&f_version=6.3.1&f_os=ubuntu&f_runtime=node+22"

// The coordinate and the result come before the picker. The page used to open
// with the instrument — a reader who arrived by a link that already named
// every dimension still had to read a grid legend to find out how it went.
func TestTheAnswerComesBeforeTheControls(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = answerStore() })
	body := get(t, mux, exactLink).Body.String()

	answer := strings.Index(body, `class="answer `)
	if answer < 0 {
		t.Fatal("an exact link renders no answer")
	}
	controls := strings.Index(body, `class="cubecontrols`)
	if controls < 0 || controls < answer {
		t.Error("the controls come before the answer they were used to reach")
	}
	// The anchor every shared link and every filter reload lands on has to
	// land on the answer, not below it.
	if cube := strings.Index(body, `id="cube"`); cube < 0 || cube > answer {
		t.Error("the answer sits outside #cube, which is where a deep link scrolls to")
	}
	mustContain(t, body, "semver.clean")
	mustContain(t, body, "@6.3.1")
	mustContain(t, body, "ubuntu · x64 · node 22 · npm")
}

// The result is a sentence with its counts named, not a glyph the reader has
// to decode. Both bases are stated, including the empty one: which side of the
// evidence a number came from is the distinction that must never blur.
func TestTheResultIsExpandedRatherThanCompressed(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = answerStore() })
	body := get(t, mux, exactLink).Body.String()

	mustContain(t, body, "1 of 1 passed")
	mustContain(t, body, "Contract verifications")
	mustContain(t, body, "Project observations")
	mustContain(t, body, "Verified — this network ran a contract at this coordinate.")
	// Nothing in the answer asks the reader to know what "≡ 100% 3" means.
	if i := strings.Index(body, `class="answer `); i >= 0 {
		card := body[i : i+strings.Index(body[i:], "</div>")]
		if strings.Contains(card, "pvratio") || strings.Contains(card, "pvpasses") {
			t.Error("the answer card still uses the grid's compressed cell notation")
		}
	}
}

// The same coordinate was readable in three places at once: the answer, the
// exact record's own header, and the control bar. It is stated once.
func TestTheCoordinateIsNotRepeated(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = answerStore() })
	body := get(t, mux, exactLink).Body.String()

	if strings.Contains(body, `class="cubeleaf"`) {
		t.Error("one record is printed as a list beside the card that already states it")
	}
	// The pinned dimensions appear as removable pins and nowhere else in the
	// control bar; the version appears in the card and in the trail, which is
	// where a reader looks to find out where they are.
	//
	// Four, since R2C-127: the page trail, the drill ladder, the answer card
	// and the pin inside the folded control bar. The ladder is the fourth and
	// it earns its place — it is the only one that names the RUNGS and says
	// this coordinate is the bottom, which is what a reader could not tell
	// from a row of chips. A fifth is still a repetition nobody asked for.
	if n := strings.Count(body, "semver.clean</span>"); n > 4 {
		t.Errorf("the pinned symbol is rendered %d times", n)
	}
}

// With the single record folded into the card, the leaf arm of the template
// stopped matching and the grid arm took over — reaching for a grid that was
// never built. What shipped under the answer was an empty table with an empty
// "×" above it: the page contradicting, in the next inch, the coordinate it
// had just stated.
func TestAStatedCoordinateRendersNoEmptyGrid(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = answerStore() })
	body := get(t, mux, exactLink).Body.String()
	mustNotContain(t, body, `<table class="pivot">`)
	mustNotContain(t, body, `class="gridpanel"`)
}

// Three visual grammars, three different things. A dimension the evidence
// decided is metadata; a dimension the reader pinned is a removable chip; a
// dimension with a real choice is a select.
func TestADecidedDimensionIsMetadataAndNotADisabledControl(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	// linux leaves the package manager decided (npm) and the arch decided
	// (x64) while version and symbol still spread.
	body := get(t, mux, "/npm/reactish?f_os=linux").Body.String()

	if strings.Contains(body, "disabled aria-disabled") {
		t.Error("a settled dimension still renders as a select the reader cannot use")
	}
	mustContain(t, body, `class="ctlfixed"`)
	mustContain(t, body, `class="ctlvalue"`)
	// The reader's own pin stays a removable chip.
	mustContain(t, body, `<span class="pinval">linux</span>`)
}

// The sentence that says what the instrument is has existed in every locale
// for as long as the cube has, and the page never rendered it: a first reader
// saw "X axis", "Y axis", "Filter" and had to infer the rest.
func TestTheCubeSaysHowItIsUsed(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	body := get(t, mux, "/npm/reactish").Body.String()

	mustContain(t, body, "Pick any two dimensions as axes")
	// The longer drill explanation stays folded with the rest of the legend:
	// it teaches the grid once and should not be scrolled past every visit.
	mustContain(t, body, "Click one to pin its coordinates")
	i := strings.Index(body, `class="home-detail gridhelp"`)
	j := strings.Index(body, "Click one to pin its coordinates")
	if i < 0 || j < i {
		t.Error("the drill note is not inside the folded legend")
	}
}

// latinRe finds a run of Latin words long enough to be a sentence rather than
// a data value: "node 22", "ubuntu", "npm" and "semver.clean" are data and
// stay as recorded, in every language.
var latinRe = regexp.MustCompile(`[A-Za-z]{3,}(?:[ ,][A-Za-z]{2,}){2,}`)

// A Korean reader's result area used to be the one place on their page
// written in English: the exact record printed pivotCell.Tip, which is
// assembled in code from phrases like "3 observations", "last seen",
// "anomaly" and "cross-checked".
func TestTheKoreanAnswerAreaCarriesNoEnglishUICopy(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = answerStore() })
	body := get(t, mux, exactLink+"&lang=ko").Body.String()

	i := strings.Index(body, `class="answer `)
	if i < 0 {
		t.Fatal("no answer card on the Korean page")
	}
	card := body[i : i+strings.Index(body[i:], "</div>")]
	// The card is translated.
	if !strings.Contains(card, "현재 조건") {
		t.Errorf("the answer card is not in the page language: %s", card)
	}
	// And carries none of the English evidence prose.
	for _, phrase := range []string{"observations", "last seen", "cross-checked", "anomaly", "passed"} {
		if strings.Contains(card, phrase) {
			t.Errorf("the Korean answer card carries the English phrase %q", phrase)
		}
	}
	if m := latinRe.FindString(stripTags(card)); m != "" {
		t.Errorf("English prose in the Korean answer: %q", m)
	}
}

// Everything the grid's tooltip carries in English prose has a named row
// here, including the two caveats that keep the numbers honest: an
// observation failure with no identified cause says a build CONTAINING this
// package broke, and a "peer" is a self-generated key rather than a machine
// or a person. Dropping either on the way out of the tooltip would have made
// the expanded record read as stronger evidence than the cell it replaced.
func TestTheAnswerNamesEveryCountItRestsOn(t *testing.T) {
	facts := []cubeFact{{
		Dims:    map[string]string{"version": "1.0.0", "symbol": "pkg.Do", "os": "ubuntu"},
		EnvHash: "e1",
		Agg: pivotAgg{
			obsPass: 8, obsFail: 2, obsAttributed: 1, obsPeers: 3, used: 5,
			verPass: 1, verFail: 0, cross: true,
			obsLastSeen: "2026-08-12T10:00:00Z", verLastSeen: "2026-08-13T10:00:00Z",
		},
	}}
	coord := map[string]string{"version": "1.0.0", "symbol": "pkg.Do", "os": "ubuntu"}
	ans := buildCubeAnswer(facts, coord, "golang", "example.com/x", "en", pivotNow, nil)
	if ans == nil {
		t.Fatal("no answer built from a fully measured coordinate")
	}
	got := map[string]string{}
	for _, f := range ans.Facts {
		got[f.Label] = f.Value
	}
	want := map[string]string{
		"Project observations":                 "8 / 10",
		"Contract verifications":               "1 / 1",
		"Usage records":                        "5",
		"Independent reporting peers":          "3",
		"Failures with a captured fingerprint": "1 / 2",
		"Cross-checked":                        "reproduced by two or more independent peers",
		// The basis decides which clock: this coordinate was verified, so
		// the date is the verification's, not the fresher observation's.
		"Last recorded": "2026-08-13",
	}
	for label, value := range want {
		if got[label] != value {
			t.Errorf("%s = %q, want %q", label, got[label], value)
		}
	}
	// A verification, however small, outranks any volume of observation: the
	// headline counts who ran it, and says so.
	if ans.Headline != "1 of 1 passed" {
		t.Errorf("headline = %q, want the verified basis", ans.Headline)
	}
	if ans.Basis != "verified" {
		t.Errorf("basis = %q", ans.Basis)
	}
}

// Presence is not a run. A coordinate whose only evidence is "the package was
// in the project" has no pass rate, and must not borrow one: usage records
// are kept out of the rate everywhere else for exactly this reason.
func TestAUsageOnlyCoordinateReportsNoRate(t *testing.T) {
	facts := []cubeFact{{
		Dims:    map[string]string{"version": "1.0.0", "symbol": cubePackageLevel},
		EnvHash: "e1",
		Agg:     pivotAgg{used: 40, obsPeers: 2},
	}}
	ans := buildCubeAnswer(facts, map[string]string{"version": "1.0.0"}, "npm", "x", "en", pivotNow, nil)
	if ans == nil {
		t.Fatal("no answer for a usage-only coordinate")
	}
	if strings.Contains(ans.Headline, "passed") {
		t.Errorf("headline = %q, but nothing ran here", ans.Headline)
	}
	for _, f := range ans.Facts {
		if f.Label == "Project observations" && !f.Absent {
			t.Errorf("observations = %q, want the absent marker", f.Value)
		}
	}
}

// stripTags leaves only what a reader actually sees, so attribute values and
// class names cannot be mistaken for visible copy.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
