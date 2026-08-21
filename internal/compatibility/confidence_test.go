package compatibility

import (
	"reflect"
	"strings"
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

// Evidence does not decay. An observation records one pinned release in one
// pinned environment at one stage, and none of those move -- that axios 1.6.0
// failed to compile on node 20 is exactly as true a year later. Halving by age
// made confidence a function of when a coordinate was last touched, and with
// verification capacity as small as it is, that was nearly every coordinate.
//
// The property is now STRUCTURAL: a Sample carries no age at all, so no
// weighting can quietly reintroduce a decay. This pins the shape.
func TestAgeDoesNotWeakenEvidence(t *testing.T) {
	typ := reflect.TypeOf(Sample{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "age") || f.Type == reflect.TypeOf(time.Duration(0)) {
			t.Errorf("Sample.%s reintroduces an age axis for a decay to hang on", f.Name)
		}
	}
}

func TestVerificationOutweighsObservation(t *testing.T) {
	// One contract PASS against three observation FAILs: verification is 10x
	// evidence, so 10 against 3.
	//
	// This used to expect above 0.9, which it only reached because the fails
	// were six months old and a half-life had quartered them. Age no longer
	// discounts anything, so the number the class weights actually produce is
	// 10/13 — and that is the property under test.
	v := Compute([]Sample{
		{Class: domain.ClassSampleVerification, Result: domain.ResultPass, Count: 1},
		{Class: domain.ClassUsageObservation, Result: domain.ResultFail, Count: 3},
	}, 2)
	if v.PassRate < 0.75 {
		t.Fatalf("pass rate %f too low: %+v", v.PassRate, v)
	}
	// The failures still count. A verification that buried them entirely
	// would be the 10x weight turning into an override.
	if v.PassRate > 0.85 {
		t.Fatalf("pass rate %f buries three real failures: %+v", v.PassRate, v)
	}
}
