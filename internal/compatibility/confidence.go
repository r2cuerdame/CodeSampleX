// Package compatibility computes confidence and aggregation views from
// evidence. Compatibility is always a derived view over Evidence/Receipts,
// never a property of a Sample (goal.md §2.3).
package compatibility

import (
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ClassWeight is the evidence-class weighting from plan contract C7:
// verification evidence outweighs co-occurrence evidence by an order of
// magnitude (goal.md §6.1).
func ClassWeight(c domain.EvidenceClass) float64 {
	switch c {
	case domain.ClassUsageObservation:
		return 1
	case domain.ClassAdoptionEvidence:
		return 3
	case domain.ClassSampleVerification, domain.ClassRuntimeInstrumentation:
		return 10
	}
	return 1
}

// Evidence does not decay, and there is deliberately no function here that
// says it does.
//
// A 90-day half-life used to weight every observation by its age. What an
// observation records is one pinned release, in one pinned environment
// bucket, at one stage -- and none of those move. That axios 1.6.0 failed to
// compile on node 20 is exactly as true a year later; what CAN change is the
// environment, and a different environment is a different coordinate with its
// own evidence.
//
// Halving instead made confidence a function of when a coordinate was last
// touched. Verification capacity is small, so most coordinates are never
// revisited: the decay applied to nearly everything and mostly reproduced
// publication order, which is the same defect this project already removed
// from search candidate selection.

// Sample is one aggregated evidence line entering a confidence computation.
// It deliberately carries no age: evidence does not decay, and a field that
// nothing reads is how a decay quietly comes back.
type Sample struct {
	Class  domain.EvidenceClass
	Result domain.Result
	Count  int64
}

// Verdict is the computed confidence for one (package, symbol, env) cell.
type Verdict struct {
	Confidence      string // HIGH | MEDIUM | LOW
	PassRate        float64
	WeightedTotal   float64
	ElevatedFailure bool
}

// Compute applies the C7 formula. independence is the count of unique peer
// buckets — simple observation counts are never used as a trust score
// (goal.md §16.5).
func Compute(samples []Sample, independence int64) Verdict {
	var wPass, wFail float64
	for _, s := range samples {
		w := ClassWeight(s.Class) * float64(s.Count)
		if s.Result == domain.ResultPass {
			wPass += w
		} else {
			wFail += w
		}
	}
	total := wPass + wFail
	v := Verdict{WeightedTotal: total}
	if total > 0 {
		v.PassRate = wPass / total
	}
	failRate := 1 - v.PassRate
	v.ElevatedFailure = total >= 5 && failRate >= 0.25
	switch {
	case v.PassRate >= 0.9 && independence >= 3 && total >= 10:
		v.Confidence = "HIGH"
	case v.PassRate >= 0.75 && independence >= 2 && total > 0:
		v.Confidence = "MEDIUM"
	default:
		v.Confidence = "LOW"
	}
	return v
}
