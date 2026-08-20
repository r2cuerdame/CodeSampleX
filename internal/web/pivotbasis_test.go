package web

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func agg(obsPass, obsFail, verPass, verFail int64) *pivotAgg {
	return &pivotAgg{obsPass: obsPass, obsFail: obsFail, verPass: verPass, verFail: verFail}
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
	if proven.Basis != "verified" {
		t.Errorf("basis = %q, want verified", proven.Basis)
	}
	if proven.Ratio != "100%" || proven.Runs != 3 {
		t.Errorf("ratio = %q of %d, want 100%% of 3", proven.Ratio, proven.Runs)
	}

	reported := buildPivotCell(agg(297, 15, 0, 0), now)
	if reported.Basis != "observed" {
		t.Errorf("basis = %q, want observed", reported.Basis)
	}
	if reported.Ratio != "95%" || reported.Runs != 312 {
		t.Errorf("ratio = %q of %d, want 95%% of 312", reported.Ratio, reported.Runs)
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
	if small.Glyph == "" {
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
// "◆ 4/4 ✓✓" — two marks carrying two meanings, one before the number and one
// after. One mark, one position, escalating.
func TestCrossVerificationEscalatesTheMarkInsteadOfAddingOne(t *testing.T) {
	now := time.Now()
	single := buildPivotCell(agg(0, 0, 4, 0), now)
	if single.Glyph != "◆" {
		t.Errorf("verified glyph = %q, want a single mark", single.Glyph)
	}

	both := agg(0, 0, 4, 0)
	both.cross = true
	crossed := buildPivotCell(both, now)
	if crossed.Glyph != "◆◆" {
		t.Errorf("cross-verified glyph = %q, want the escalated mark", crossed.Glyph)
	}
	if !crossed.Cross {
		t.Error("the cross flag must survive for the legend and the label")
	}
	// An observation-only cell carries no mark here at all; it is already
	// marked weak by the ? it always had.
	if reported := buildPivotCell(agg(300, 12, 0, 0), now); reported.Glyph != "" {
		t.Errorf("observed glyph = %q, want no mark", reported.Glyph)
	}
}

// The mark says who ran it, never how it went. A check means "passed"
// everywhere it appears, so a cell we ran and that failed rendered as a tick
// beside 0/1 in red — the verdict smuggled back into the glyph after being
// taken out of the word.
func TestTheVerificationMarkIsNotAVerdict(t *testing.T) {
	now := time.Now()
	failed := buildPivotCell(agg(0, 0, 0, 3), now)
	if failed.Glyph == "✓" || failed.Glyph == "✓✓" {
		t.Errorf("a cell that failed carries a pass mark: %q with rate %q",
			failed.Glyph, failed.Ratio)
	}
	passed := buildPivotCell(agg(0, 0, 3, 0), now)
	if failed.Glyph != passed.Glyph {
		t.Errorf("the mark changed with the outcome: failed %q, passed %q — it marks the basis",
			failed.Glyph, passed.Glyph)
	}
	if failed.Ratio != "0%" || passed.Ratio != "100%" {
		t.Errorf("the rate must carry the outcome: %q and %q", failed.Ratio, passed.Ratio)
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
