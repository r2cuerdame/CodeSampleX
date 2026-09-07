package web

import (
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
)

// backgroundRetryReady is called while the owning cache mutex is held. A
// terminal series stays closed until the cache's ordinary freshness interval
// has elapsed; only then does a request open a new series.
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

func backgroundRetryFailed(series *retrypolicy.Series, next *time.Time, now time.Time,
	deferFor time.Duration, draw func(time.Duration) time.Duration) {
	retry, state := series.Failure()
	if state == retrypolicy.Waiting {
		delay, _ := retrypolicy.Delay(retry, draw)
		*next = now.Add(delay)
		return
	}
	if deferFor <= 0 {
		deferFor = 5 * time.Minute
	}
	*next = now.Add(deferFor)
}

func backgroundRetrySucceeded(series *retrypolicy.Series, next *time.Time) {
	series.Reset()
	*next = time.Time{}
}

func (s *site) backgroundNowTime() time.Time {
	if s.backgroundNow != nil {
		return s.backgroundNow()
	}
	return time.Now()
}
