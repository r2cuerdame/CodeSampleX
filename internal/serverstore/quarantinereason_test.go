package serverstore

import (
	"context"
	"testing"
)

// 1,113 samples are quarantined in production and every one of them carries a
// reason, written at the moment it was withdrawn. None of it reached the
// operator: seeing why anything was pulled meant opening a database. A count
// with no reason beside it cannot be acted on — "983 quarantined" is alarming
// and "983 duplicate coordinates, superseded" is a finished piece of work.
func TestFarmHealthReportsWhyThingsWereWithdrawn(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	for _, s := range []struct{ id, reason string }{
		{"sha256:dup1", "duplicate coordinate: superseded by the kept sample"},
		{"sha256:dup2", "duplicate coordinate: superseded by the kept sample"},
		{"sha256:draft", "private authoring draft awaiting cross verification"},
		{"sha256:bare", ""},
	} {
		if err := f.SaveSample(ctx, SampleRow{
			SampleID: s.id, ManifestJSON: `{"packages":[],"symbols":[]}`,
			Quarantined: true, QuarantineReason: s.reason,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.SaveSample(ctx, SampleRow{
		SampleID: "sha256:live", ManifestJSON: `{"packages":[],"symbols":[]}`,
	}); err != nil {
		t.Fatal(err)
	}

	health, err := f.FarmHealthNow(ctx, f.now())
	if err != nil {
		t.Fatal(err)
	}
	if health.QuarantinedByReason["duplicate coordinate: superseded by the kept sample"] != 2 {
		t.Errorf("reasons = %v, want the duplicate reason counted twice", health.QuarantinedByReason)
	}
	if health.QuarantinedByReason["private authoring draft awaiting cross verification"] != 1 {
		t.Errorf("reasons = %v, want the draft reason", health.QuarantinedByReason)
	}
	// A withdrawal with no reason is the one worth surfacing, not hiding:
	// something was pulled and nobody wrote down why.
	if health.QuarantinedByReason[""] != 1 {
		t.Errorf("reasons = %v, want the unexplained withdrawal counted", health.QuarantinedByReason)
	}
	// A live sample is not a withdrawal.
	total := 0
	for _, n := range health.QuarantinedByReason {
		total += n
	}
	if total != 4 {
		t.Errorf("total quarantined = %d, want 4", total)
	}
}
