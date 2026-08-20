package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A conditional GET that comes back 304 did almost no work: the client sent
// an ETag, the server compared it and sent nothing. Charging that the same as
// a search is backwards — the polite client pays the same as the expensive
// one, and in production it is what actually exhausted the budget. Of 4,074
// shard requests in one window 2,606 were 304, and 345 real ones were refused
// while the revalidations sailed through.
func TestNotModifiedIsRefunded(t *testing.T) {
	l := newLimiter(rate{burst: 2, per: time.Minute})
	a := &api{}
	h := a.limit(l, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusNotModified {
			t.Fatalf("request %d = %d, want 304 every time", i, rec.Code)
		}
	}
}

// Everything else still costs a token: the refund is for work not done, not
// a hole in the limit.
func TestRealResponsesStillSpendTheBudget(t *testing.T) {
	l := newLimiter(rate{burst: 2, per: time.Minute})
	a := &api{}
	h := a.limit(l, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	codes := make([]int, 0, 3)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		codes = append(codes, rec.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != http.StatusTooManyRequests {
		t.Errorf("codes = %v, want two through then a 429", codes)
	}
}

// A handler that writes a body without calling WriteHeader still answered 200
// and must still be charged.
func TestAnImplicitTwoHundredIsCharged(t *testing.T) {
	l := newLimiter(rate{burst: 1, per: time.Minute})
	a := &api{}
	h := a.limit(l, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("body"))
	})
	first := httptest.NewRecorder()
	h(first, httptest.NewRequest(http.MethodGet, "/x", nil))
	second := httptest.NewRecorder()
	h(second, httptest.NewRequest(http.MethodGet, "/x", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Errorf("second = %d, want 429", second.Code)
	}
}

// A verifier polling its own queue is not a search storm, and it was sharing
// the read budget with shard and registry traffic. In production 1,145 job
// listings competed with 4,074 shard requests against one 300/min bucket
// keyed by address, and the fleet throttled itself out of doing the work it
// was asking for: 57 job listings refused, 345 shard reads refused, and the
// verifier stopped entirely.
func TestQueuePollingHasItsOwnBudget(t *testing.T) {
	lim := newLimiters()
	if lim.queue == nil {
		t.Fatal("no queue budget")
	}
	if lim.queue == lim.read {
		t.Fatal("queue polling still shares the read budget")
	}
	if queueLimit.burst <= readLimit.burst/4 {
		t.Errorf("queue burst %d is too small to poll against", queueLimit.burst)
	}
}
