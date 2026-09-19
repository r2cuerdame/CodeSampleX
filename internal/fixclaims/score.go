package fixclaims

import (
	"math"
	"regexp"
	"time"
)

// ScoreInputs are the facts the queue knows about a candidate beyond the
// candidate itself. Every one is optional; a zero input contributes zero.
type ScoreInputs struct {
	// Asks is how many times this package has been asked for (WANTED rows,
	// search misses) -- demand the network already measured.
	Asks int64
	// Dependents is how many child coordinates the dependency atlas hangs
	// off this package.
	Dependents int64
	// FingerprintMatch says a failure fingerprint the network already
	// observed matches the candidate's hint: the reproducer may exist.
	FingerprintMatch bool
	// SampleReuse says a published sample already exercises the symbol.
	SampleReuse bool
	Now         time.Time
}

// Score weights. They are one scale, and the two largest terms are the two
// that say "Farm may not have to write anything": a fingerprint the network
// already observed, and a sample that already exercises the behaviour.
const (
	scoreConfidenceHigh   = 30
	scoreConfidenceMedium = 15
	scoreConfidenceLow    = 5
	scoreSeverityEach     = 20
	scoreSeverityCap      = 40
	scoreFingerprintMatch = 100
	scoreSampleReuse      = 50
	scoreUpstreamRepro    = 25
	scoreAskEach          = 10
	scoreAskCap           = 50
	scoreFreshMonth       = 20
	scoreFreshQuarter     = 10
)

var severity = regexp.MustCompile(`(?i)\b(crash(es|ed|ing)?|panics?|data loss|loses? data|corrupt(s|ed|ion)?|build (fail|break)|fails? to (build|compile|install)|compatibilit|regression|segfault|deadlock|hangs?|memory leak|security|infinite loop|stack overflow)\b`)

// Score ranks a candidate for the bounded queue. Higher runs first. It is
// deterministic in its inputs so the same queue reads the same on every
// server.
func Score(c Candidate, in ScoreInputs) int64 {
	var s int64
	switch c.Confidence {
	case ConfidenceHigh:
		s += scoreConfidenceHigh
	case ConfidenceMedium:
		s += scoreConfidenceMedium
	default:
		s += scoreConfidenceLow
	}
	sev := int64(len(severity.FindAllStringIndex(c.Claim, -1))) * scoreSeverityEach
	if sev > scoreSeverityCap {
		sev = scoreSeverityCap
	}
	s += sev
	if in.FingerprintMatch {
		s += scoreFingerprintMatch
	}
	if in.SampleReuse {
		s += scoreSampleReuse
	}
	if c.UpstreamReproducer {
		s += scoreUpstreamRepro
	}
	asks := in.Asks * scoreAskEach
	if asks > scoreAskCap {
		asks = scoreAskCap
	}
	s += asks
	if in.Dependents > 0 {
		s += int64(math.Log2(float64(in.Dependents)+1)) * 5
	}
	if !c.ReleasedAt.IsZero() && !in.Now.IsZero() {
		switch age := in.Now.Sub(c.ReleasedAt); {
		case age < 0:
		case age <= 30*24*time.Hour:
			s += scoreFreshMonth
		case age <= 90*24*time.Hour:
			s += scoreFreshQuarter
		}
	}
	return s
}
