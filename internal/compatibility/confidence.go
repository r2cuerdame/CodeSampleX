// Package compatibility computes confidence and aggregation views from
// evidence. Compatibility is always a derived view over Evidence/Receipts,
// never a property of a Sample (goal.md §2.3).
package compatibility

import (
	"math"
	"time"

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

// RecencyDecay halves an observation's weight every 90 days.
func RecencyDecay(age time.Duration) float64 {
	days := age.Hours() / 24
	if days < 0 {
		days = 0
	}
	return math.Pow(0.5, days/90)
}

// Sample is one aggregated evidence line entering a confidence computation.
type Sample struct {
	Class  domain.EvidenceClass
	Result domain.Result
	Count  int64
	Age    time.Duration
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
		w := ClassWeight(s.Class) * float64(s.Count) * RecencyDecay(s.Age)
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
