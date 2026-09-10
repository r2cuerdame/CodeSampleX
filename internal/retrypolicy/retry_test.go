package retrypolicy

import (
	"testing"
	"time"
)

func TestDelayScheduleAndJitterBounds(t *testing.T) {
	for retry, base := range []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
	} {
		n := retry + 1
		low, ok := Delay(n, func(time.Duration) time.Duration { return 0 })
		if !ok || low != base {
			t.Fatalf("retry %d low delay = %s, %v; want %s, true", n, low, ok, base)
		}
		high, ok := Delay(n, func(max time.Duration) time.Duration { return max })
		if !ok || high != base+base/4 {
			t.Fatalf("retry %d high delay = %s, %v; want %s, true", n, high, ok, base+base/4)
		}
	}
	if _, ok := Delay(0, nil); ok {
		t.Fatal("retry zero unexpectedly accepted")
	}
	if _, ok := Delay(MaxRetries+1, nil); ok {
		t.Fatal("sixth retry unexpectedly accepted")
	}
}

func TestSeriesStopsAfterFiveRetriesInTerminalDeferredState(t *testing.T) {
	var s Series
	for want := 1; want <= MaxRetries; want++ {
		retry, state := s.Failure()
		if retry != want || state != Waiting {
			t.Fatalf("failure %d = retry %d state %d, want retry %d waiting", want, retry, state, want)
		}
	}
	if retry, state := s.Failure(); retry != 0 || state != FailedDeferred {
		t.Fatalf("post-exhaustion = retry %d state %d, want terminal deferred", retry, state)
	}
	if retry, state := s.Failure(); retry != 0 || state != FailedDeferred {
		t.Fatalf("terminal series restarted itself: retry %d state %d", retry, state)
	}
	s.Reset()
	if retry, state := s.Failure(); retry != 1 || state != Waiting {
		t.Fatalf("reset series = retry %d state %d, want fresh retry 1", retry, state)
	}
}
