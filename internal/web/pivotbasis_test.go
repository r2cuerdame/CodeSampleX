package web

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// agg builds a cell aggregate. Observations carry a peer-bucket count
// because that is what the cell weighs its rate by — the event count says
// how many builds ran, not how many machines ran them.
func agg(obsPass, obsFail, verPass, verFail int64) *pivotAgg {
	a := &pivotAgg{obsPass: obsPass, obsFail: obsFail, verPass: verPass, verFail: verFail}
	if obsPass+obsFail > 0 {
		a.obsPeers = 4
	}
	return a
}

// A verdict word claims more than was measured. "PASS" reads as "this works
// here"; what was measured is "3 runs, 3 passed". The cell states the rate
// and names only its BASIS — who ran it — so the product never puts a
// judgement where it only has a count.
func TestPivotCellCarriesBasisAndRateNotAVerdict(t *testing.T) {
	now := time.Now()
	// "observed" survives as a BASIS token -- it names who ran it, not how it
	// went -- so only the outcome words are forbidden.
	for _, verdict := range []string{"PASS", "FAIL", "MIXED", "OBSERVED"} {
		for _, a := range []*pivotAgg{agg(0, 0, 3, 0), agg(0, 0, 0, 2), agg(0, 0, 5, 3), agg(297, 15, 0, 0)} {
			cell := buildPivotCell(a, now)
			if cell.Basis == verdict {
				t.Errorf("basis still speaks a verdict %q: %+v", verdict, cell)
			}
			if lower := strings.ToLower(verdict); lower != "observed" && cell.Class == lower {
				t.Errorf("class still speaks a verdict %q: %+v", verdict, cell)
			}
		}
	}

	proven := buildPivotCell(agg(0, 0, 3, 0), now)
	if proven.Basis != "verified" || proven.Glyph != "✓" {
		t.Errorf("basis = %q glyph = %q, want verified with the mark", proven.Basis, proven.Glyph)
	}
	// Our own runs are the mark, not the count: nobody has been seen using
	// this, and the cell says so instead of reporting our runs as usage.
	if proven.Ratio != "—" {
		t.Errorf("ratio = %q, want no usage recorded", proven.Ratio)
	}

	reported := buildPivotCell(agg(297, 15, 0, 0), now)
	if reported.Basis != "observed" || reported.Glyph != "" {
		t.Errorf("basis = %q glyph = %q, want observed with no mark", reported.Basis, reported.Glyph)
	}
	if reported.Ratio != "95%" || reported.Passes != "312" || reported.Runs != 312 {
		t.Errorf("cell = %q %q of %d, want 95%% of the 312 builds it rests on",
			reported.Ratio, reported.Passes, reported.Runs)
	}
}

// Observation counts dwarf verification counts — production carries 9,220
// observation rows against a far smaller receipt corpus. If both tiers render
// as bare ratios the anonymous cell looks MORE authoritative than the proven
// one, which is exactly backwards, so the basis must never blur.
func TestALargeObservationRatioNeverBorrowsTheVerifiedBasis(t *testing.T) {
	now := time.Now()
	big := buildPivotCell(agg(50000, 1, 0, 0), now)
	small := buildPivotCell(agg(0, 0, 1, 0), now)

	if big.Basis == small.Basis {
		t.Fatalf("50,000 reports and one verified run share basis %q", big.Basis)
	}
	if big.Glyph == small.Glyph {
		t.Errorf("both bases render as %q — basis must be legible without colour", big.Glyph)
	}
	// The mark follows the basis, not the size: absence of a mark is what
	// says "reported, not proven", however large the number beside it.
	if big.Glyph != "" {
		t.Errorf("a 50,000-report cell carries a verification mark %q", big.Glyph)
	}
	if small.Glyph != "✓" {
		t.Error("a single verified run lost its mark to a larger observed one")
	}
}

// A cell with nothing recorded must stay distinguishable from every measured
// cell, however small the measurement.
func TestEmptyCellHasNoBasis(t *testing.T) {
	cell := buildPivotCell(agg(0, 0, 0, 0), time.Now())
	if cell.Basis != "" || cell.Class != "empty" {
		t.Errorf("empty cell = %+v", cell)
	}
	if cell.Ratio != "" {
		t.Errorf("empty cell shows a rate %q", cell.Ratio)
	}
}

// Cross verification implies verification, so two marks for it rendered
// two marks carrying two meanings, one before the number and one after.
// Cross reproduction is a strictly better version of the same fact, not a
// different one, so it earns no second symbol. The grid carries at most one
// glyph, and the tooltip carries the rest.
func TestCrossReproductionAddsNoSecondMark(t *testing.T) {
	now := time.Now()
	both := agg(0, 0, 4, 0)
	both.cross = true
	crossed := buildPivotCell(both, now)
	if crossed.Glyph != "✓" {
		t.Errorf("cross-verified glyph = %q, want the single mark", crossed.Glyph)
	}
	if !crossed.Cross {
		t.Error("the cross flag must survive for the tooltip")
	}
	if !strings.Contains(crossed.Tip, "cross-checked") {
		t.Errorf("tooltip %q lost the cross-verification fact", crossed.Tip)
	}
}
func TestTheVerificationMarkIsNotAVerdict(t *testing.T) {
	now := time.Now()
	failed := buildPivotCell(agg(0, 0, 0, 3), now)
	if failed.Glyph != "✕" {
		t.Errorf("a failed run = %q, want the check's counterpart", failed.Glyph)
	}
	passed := buildPivotCell(agg(0, 0, 3, 0), now)
	if passed.Glyph != "✓" {
		t.Errorf("a clean run lost its mark: %q", passed.Glyph)
	}
	if failed.Tone != "fail" || passed.Tone != "pass" {
		t.Errorf("tone must carry the outcome: %q and %q", failed.Tone, passed.Tone)
	}
	if failed.Ratio != "—" || passed.Ratio != "—" {
		t.Errorf("neither verified cell has usage to report: %q and %q", failed.Ratio, passed.Ratio)
	}
}

// A stray NUL byte reached the stylesheet once, through an escape that Python
// read as octal rather than as CSS. It rendered as a broken glyph beside
// every run count and turned a text asset into a binary one.
func TestStylesheetIsCleanText(t *testing.T) {
	css, err := staticFS.ReadFile("static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	if i := bytes.IndexByte(css, 0); i >= 0 {
		t.Errorf("site.css carries a NUL byte at offset %d", i)
	}
	if !utf8.Valid(css) {
		t.Error("site.css is not valid UTF-8")
	}
}

// Go's major has not moved in a decade, so bucketing by it collapsed every Go
// version into one meaningless "go 1" column — and beside a versionless "go"
// row it read as the same runtime listed twice.
func TestRuntimeBucketsWhereTheRuntimeActuallyBreaks(t *testing.T) {
	for _, tc := range []struct{ runtime, version, want string }{
		{"go", "1.26", "go 1.26"},
		{"go", "1.25.3", "go 1.25"},
		{"python", "3.12.1", "python 3.12"},
		// node and bun really do break on the major.
		{"node", "22.18", "node 22"},
		{"bun", "1.3.14", "bun 1.3"},
	} {
		if got := runtimeBucket(tc.runtime, tc.version); got != tc.want {
			t.Errorf("%s %s bucketed as %q, want %q", tc.runtime, tc.version, got, tc.want)
		}
	}
	// An unrecorded version says so. Rendering a bare "go" beside "go 1.26"
	// reads as a duplicate rather than as the gap it is.
	if got := runtimeBucket("go", ""); got != "go (version not recorded)" {
		t.Errorf("versionless runtime = %q, want it to name the gap", got)
	}
	if got := runtimeBucket("", "1.26"); got != "" {
		t.Errorf("no runtime should bucket to nothing, got %q", got)
	}
}
