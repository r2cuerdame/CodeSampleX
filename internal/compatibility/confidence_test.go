package compatibility

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestComputeHigh(t *testing.T) {
	v := Compute([]Sample{
		{Class: domain.ClassSampleVerification, Result: domain.ResultPass, Count: 2},
		{Class: domain.ClassUsageObservation, Result: domain.ResultPass, Count: 20},
	}, 5)
	if v.Confidence != "HIGH" || v.ElevatedFailure {
		t.Fatalf("want HIGH, got %+v", v)
	}
}

func TestComputeIndependenceGate(t *testing.T) {
	// Massive counts from a single peer must not reach HIGH (goal.md §16.5).
	v := Compute([]Sample{
		{Class: domain.ClassUsageObservation, Result: domain.ResultPass, Count: 100000},
	}, 1)
	if v.Confidence == "HIGH" {
		t.Fatalf("single-peer evidence must not be HIGH: %+v", v)
	}
}

func TestComputeElevatedFailure(t *testing.T) {
	v := Compute([]Sample{
		{Class: domain.ClassUsageObservation, Result: domain.ResultPass, Count: 6},
		{Class: domain.ClassUsageObservation, Result: domain.ResultFail, Count: 3},
	}, 4)
	if !v.ElevatedFailure {
		t.Fatalf("33%% failure over 9 weighted obs must flag elevated failure: %+v", v)
	}
}

func TestRecencyDecayHalvesAt90Days(t *testing.T) {
	got := RecencyDecay(90 * 24 * time.Hour)
	if got < 0.49 || got > 0.51 {
		t.Fatalf("decay at 90d = %f, want ~0.5", got)
	}
	if RecencyDecay(0) != 1 {
		t.Fatal("no decay at age 0")
	}
}

func TestVerificationOutweighsObservation(t *testing.T) {
	// One contract PASS against a few stale observation FAILs should keep a
	// decent pass rate — verification is 10x evidence.
	v := Compute([]Sample{
		{Class: domain.ClassSampleVerification, Result: domain.ResultPass, Count: 1},
		{Class: domain.ClassUsageObservation, Result: domain.ResultFail, Count: 3, Age: 180 * 24 * time.Hour},
	}, 2)
	if v.PassRate < 0.9 {
		t.Fatalf("pass rate %f too low: %+v", v.PassRate, v)
	}
}
