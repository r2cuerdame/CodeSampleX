package web

import (
	"strings"
	"testing"
	"time"
)

// The number beside the rate is meant to say how much the rate rests on, and
// the code's own comment said "how many real machines got through it". It
// printed the observation event count instead. For pgx v5.10.0 on go 1.26
// that read "85% 1154" from ONE reporting peer bucket building the same two
// projects over and over: 1,154 looks like 1,154 independent confirmations
// and is one machine's build log.
func TestCellWeighsMachinesNotEvents(t *testing.T) {
	a := &pivotAgg{obsPass: 1154, obsFail: 207, obsPeers: 1, conf: 2}
	cell := buildPivotCell(a, time.Now())

	if cell.Ratio != "85%" {
		t.Errorf("ratio = %q, want 85%%", cell.Ratio)
	}
	if cell.Passes == "1154" {
		t.Fatal("the cell still weighs the rate by raw observation events")
	}
	if cell.Passes != "1" {
		t.Errorf("passes = %q, want the 1 peer bucket that reported", cell.Passes)
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
	if !strings.Contains(cell.Tip, "1361 build observations") {
		t.Errorf("tip = %q, want it to name the events as builds", cell.Tip)
	}
	if !strings.Contains(cell.Tip, "1 reporting machine") {
		t.Errorf("tip = %q, want it to state how many machines reported", cell.Tip)
	}
}
