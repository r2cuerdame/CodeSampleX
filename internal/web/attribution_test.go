package web

import (
	"strings"
	"testing"
	"time"
)

// A cell's observation rate is co-occurrence: it says what happened in builds
// that CONTAINED this package, not what this package did. 82% of production's
// failures carry no error code, and one of them wrote a FAIL for all 412
// packages in a lockfile.
//
// The rate keeps them — the build really did fail — so what the tooltip has
// to say is how many of those failures anyone could name a cause for. Without
// it, a package that breaks and a package that was merely installed when
// something else broke read identically.
func TestTooltipSaysHowManyFailuresHadAnIdentifiedCause(t *testing.T) {
	cell := buildPivotCell(&pivotAgg{
		obsPass: 30, obsFail: 10, obsAttributed: 2, obsPeers: 1,
	}, time.Now())

	if cell.Ratio != "75%" {
		t.Errorf("ratio = %q, want 75%% — uncoded failures stay in the rate", cell.Ratio)
	}
	if !strings.Contains(cell.Tip, "2 of 10 failures with an identified cause") {
		t.Errorf("tip = %q, want the attribution split", cell.Tip)
	}
}

// When every failure was attributable there is nothing to warn about, and a
// tooltip that qualifies everything qualifies nothing.
func TestTooltipStaysQuietWhenEveryFailureWasAttributed(t *testing.T) {
	cell := buildPivotCell(&pivotAgg{
		obsPass: 30, obsFail: 4, obsAttributed: 4, obsPeers: 1,
	}, time.Now())
	if strings.Contains(cell.Tip, "identified cause") {
		t.Errorf("tip = %q, want no attribution note when all four were named", cell.Tip)
	}
}

// No failures at all: nothing to say either.
func TestTooltipStaysQuietWithNoFailures(t *testing.T) {
	cell := buildPivotCell(&pivotAgg{obsPass: 30, obsPeers: 1}, time.Now())
	if strings.Contains(cell.Tip, "identified cause") {
		t.Errorf("tip = %q, want no attribution note", cell.Tip)
	}
}
