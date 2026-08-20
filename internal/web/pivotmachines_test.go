package web

import (
	"strings"
	"testing"
	"time"
)

// The number beside the rate is the total that rate rests on — its
// denominator — because a percentage alone throws away how much evidence it
// carries, and 85% of a thousand is not 85% of three.
//
// It has been wrong twice. It printed the PASS count (1,154 beside 85%),
// which is the numerator and not what the rate divides by; then it printed
// the peer-bucket count (1 beside 85%), which is not part of the division at
// all and read as "85% of one". How many machines reported is a real and
// separate fact, and it belongs in the tooltip beside the rest of them.
func TestCellWeighsTheRateByWhatItRestsOn(t *testing.T) {
	a := &pivotAgg{obsPass: 1154, obsFail: 207, obsPeers: 1, conf: 2}
	cell := buildPivotCell(a, time.Now())

	if cell.Ratio != "85%" {
		t.Errorf("ratio = %q, want 85%%", cell.Ratio)
	}
	if cell.Passes != "1361" {
		t.Errorf("passes = %q, want 1361 — the total the 85%% divides", cell.Passes)
	}
	if cell.Runs != 1361 {
		t.Errorf("runs = %d, want 1361", cell.Runs)
	}
}

// Peer buckets are a peak, never a sum: the same machine across two epochs
// is one machine, which is the rule the producer already applies and the one
// the whole participation figure rests on.
func TestPeerBucketsPeakRatherThanSum(t *testing.T) {
	var a pivotAgg
	a.mergeObservations(pivotAgg{obsPass: 10, obsPeers: 3})
	a.mergeObservations(pivotAgg{obsPass: 10, obsPeers: 2})
	if a.obsPeers != 3 {
		t.Errorf("obsPeers = %d, want 3 — buckets peak, they do not sum", a.obsPeers)
	}
	if a.obsPass != 20 {
		t.Errorf("obsPass = %d, want 20 — events do sum", a.obsPass)
	}
}

// The tooltip has room to be explicit, and must not let "observed" be read
// as a count of people.
func TestTooltipNamesObservationsAsBuilds(t *testing.T) {
	cell := buildPivotCell(&pivotAgg{obsPass: 1154, obsFail: 207, obsPeers: 1}, time.Now())
	if !strings.Contains(cell.Tip, "1361 observations") {
		t.Errorf("tip = %q, want it to name the events as observations", cell.Tip)
	}
	if !strings.Contains(cell.Tip, "1 reporting peer") {
		t.Errorf("tip = %q, want it to state how many peers reported", cell.Tip)
	}
}
