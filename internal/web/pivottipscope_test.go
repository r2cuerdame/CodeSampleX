package web

import (
	"strings"
	"testing"
	"time"
)

// The cell read "≡ —" and the author said it could not be true, because they
// had run the code themselves while writing it.
//
// They were right, and the tooltip was wrong. It said "no builds observed from
// anyone else", which is a claim about the world: that nobody out there has
// built this. What the zero actually means is narrower and duller — no
// observation was ATTRIBUTED to this coordinate. The package underneath had
// thousands of observation events at the time; the symbol row had none,
// because attribution needs a scan to tie a build to a symbol.
//
// So the tip states what the number counts and stops there. Anything about
// who did or did not build this belongs to people we never measured.
func TestTipDoesNotClaimNobodyElseBuiltIt(t *testing.T) {
	own := buildPivotCell(&pivotAgg{verPass: 2}, time.Now())

	if strings.Contains(own.Tip, "anyone else") {
		t.Errorf("tip = %q, want no claim about who else built it", own.Tip)
	}
	if !strings.Contains(own.Tip, "no observations at this coordinate") {
		t.Errorf("tip = %q, want the zero named as what it counts", own.Tip)
	}
	// The half that IS ours keeps its words.
	if !strings.Contains(own.Tip, "2 verified") {
		t.Errorf("tip = %q, want our own runs still counted", own.Tip)
	}
}

// A coordinate that HAS observations says nothing of the sort — the clause is
// about an absence, and there is none to report.
func TestTipOmitsTheAbsenceClauseWhenObservationsExist(t *testing.T) {
	seen := buildPivotCell(&pivotAgg{obsPass: 4, obsPeers: 1, verPass: 2}, time.Now())
	if strings.Contains(seen.Tip, "no observations at this coordinate") {
		t.Errorf("tip = %q, want no absence clause when observations exist", seen.Tip)
	}
}
