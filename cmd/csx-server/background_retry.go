package main

import (
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
)

// backgroundRetryReady is called with the cache's mutex held. A terminal
// retry series can only be reopened after its normal refresh interval, never
// by the next request that happens to observe the failure.
func backgroundRetryReady(series *retrypolicy.Series, next *time.Time, now time.Time) bool {
	if now.Before(*next) {
		return false
	}
	if series.State() == retrypolicy.FailedDeferred {
		series.Reset()
		*next = time.Time{}
	}
	return true
}

func backgroundRetryFailed(
	series *retrypolicy.Series,
	next *time.Time,
	now time.Time,
	deferFor time.Duration,
) retrypolicy.State {
	retry, state := series.Failure()
	if state == retrypolicy.Waiting {
		delay, _ := retrypolicy.Delay(retry, nil)
		*next = now.Add(delay)
		return state
	}
	if deferFor <= 0 {
		deferFor = 5 * time.Minute
	}
	*next = now.Add(deferFor)
	return state
}

func backgroundRetrySucceeded(series *retrypolicy.Series, next *time.Time) {
	series.Reset()
	*next = time.Time{}
}
