// Package retrypolicy defines the one bounded retry shape used by background
// work. Keeping the schedule here prevents each cache or worker from growing
// its own unbounded retry loop.
package retrypolicy

import (
	"math/rand/v2"
	"time"
)

const MaxRetries = 5

type State uint8

const (
	Ready State = iota
	Waiting
	FailedDeferred
)

// Series counts one initial attempt and at most MaxRetries follow-up attempts.
// Once the fifth retry fails, the series is terminal until its owner explicitly
// defers and resets it for a later normal scheduling window.
type Series struct {
	retries int
	state   State
}

func (s *Series) Failure() (retry int, state State) {
	if s.state == FailedDeferred {
		return 0, FailedDeferred
	}
	if s.retries >= MaxRetries {
		s.state = FailedDeferred
		return 0, s.state
	}
	s.retries++
	s.state = Waiting
	return s.retries, s.state
}

func (s *Series) State() State { return s.state }

func (s *Series) Reset() {
	s.retries = 0
	s.state = Ready
}

// Delay returns 1s, 2s, 4s, 8s, or 16s plus positive jitter bounded to 25%
// of that base. retry is one-based. Values outside the supported range are
// rejected so callers cannot accidentally create a sixth retry.
func Delay(retry int, draw func(max time.Duration) time.Duration) (time.Duration, bool) {
	if retry < 1 || retry > MaxRetries {
		return 0, false
	}
	base := time.Second << (retry - 1)
	maxJitter := base / 4
	var jitter time.Duration
	if draw == nil {
		jitter = time.Duration(rand.Int64N(int64(maxJitter) + 1))
	} else {
		jitter = draw(maxJitter)
		if jitter < 0 {
			jitter = 0
		}
		if jitter > maxJitter {
			jitter = maxJitter
		}
	}
	return base + jitter, true
}
