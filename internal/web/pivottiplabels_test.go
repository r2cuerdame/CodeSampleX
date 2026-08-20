package web

import (
	"strings"
	"testing"
	"time"
)

// One cell showed "85%" on its face and "pass 100%" in its tooltip, and
// neither said which was which. They measure different things — the face is
// how often the world's builds passed, the tooltip figure is how often our
// own sandboxed runs did — so each names its basis.
func TestTooltipNamesWhichPassRateItIs(t *testing.T) {
	both := buildPivotCell(&pivotAgg{
		obsPass: 1154, obsFail: 207, obsPeers: 1, verPass: 132,
	}, time.Now())
	if !strings.Contains(both.Tip, "observed pass 85%") {
		t.Errorf("tip = %q, want the observed rate named", both.Tip)
	}
	if !strings.Contains(both.Tip, "verified pass 100%") {
		t.Errorf("tip = %q, want the verified rate named", both.Tip)
	}
	// And the face keeps reporting the world, not our farm.
	if both.Ratio != "85%" {
		t.Errorf("ratio = %q, want the observed rate on the face", both.Ratio)
	}
}
