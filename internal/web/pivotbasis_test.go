package web

import (
	"strings"
	"testing"
	"time"
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
	if proven.Ratio != "3/3" {
		t.Errorf("ratio = %q, want 3/3 — the rate is the result", proven.Ratio)
	}

	reported := buildPivotCell(agg(297, 15, 0, 0), now)
	if reported.Basis != "observed" {
		t.Errorf("basis = %q, want observed", reported.Basis)
	}
	if reported.Ratio != "297/312" {
		t.Errorf("ratio = %q, want 297/312", reported.Ratio)
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
	// Weak-evidence marking still follows the basis, not the size.
	if !big.Maybe {
		t.Error("an observation-only cell lost its weak-evidence marker")
	}
	if small.Maybe {
		t.Error("a verified cell gained a weak-evidence marker")
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
