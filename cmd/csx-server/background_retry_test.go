package main

import (
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
)

func TestBackgroundRetryGateDefersAfterFiveRetries(t *testing.T) {
	var series retrypolicy.Series
	var next time.Time
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	for attempt := 0; attempt < 1+retrypolicy.MaxRetries; attempt++ {
		if !backgroundRetryReady(&series, &next, now) {
			t.Fatalf("attempt %d was not ready", attempt+1)
		}
		backgroundRetryFailed(&series, &next, now, 5*time.Minute)
		if attempt < retrypolicy.MaxRetries {
			now = next.Add(time.Nanosecond)
		}
	}
	if series.State() != retrypolicy.FailedDeferred {
		t.Fatalf("state = %d, want failed/deferred", series.State())
	}
	if backgroundRetryReady(&series, &next, now) {
		t.Fatal("exhausted work was immediately ready")
	}
	now = next.Add(time.Nanosecond)
	if !backgroundRetryReady(&series, &next, now) || series.State() != retrypolicy.Ready {
		t.Fatal("deferred work did not reopen in a fresh state")
	}
}
