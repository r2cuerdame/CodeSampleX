package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type outcomeRecordingStore struct {
	serverstore.Store
	mu       sync.Mutex
	outcomes []serverstore.SearchOutcome
	times    []time.Time
	err      error
}

func (s *outcomeRecordingStore) RecordSearchOutcome(_ context.Context, at time.Time, outcome serverstore.SearchOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.outcomes = append(s.outcomes, outcome)
	s.times = append(s.times, at)
	return nil
}

func TestSuccessfulPublicSearchesRecordOnlyAggregateOutcome(t *testing.T) {
	var recorder *outcomeRecordingStore
	srv, fake, _ := newTestServer(t, func(d *Deps) {
		recorder = &outcomeRecordingStore{Store: d.Store}
		d.Store = recorder
	})
	saveTestSample(t, fake, "PUBLISHED")

	var hit domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v1/search", domain.SearchRequest{
		Query: "post JSON with axios", Packages: []string{"pkg:npm/axios@1.12.0"},
		Symbols: []string{"axios.post"}, Environment: nodeEnv("esm"),
	}, &hit)
	if resp.StatusCode != http.StatusOK || hit.Miss {
		t.Fatalf("hit response = status:%d miss:%v", resp.StatusCode, hit.Miss)
	}

	var miss domain.SearchResponse
	resp = postJSON(t, srv.URL+"/v2/search", domain.SearchRequest{
		Query: "unrelated quantum teapot", Environment: nodeEnv("esm"),
	}, &miss)
	if resp.StatusCode != http.StatusOK || !miss.Miss {
		t.Fatalf("miss response = status:%d miss:%v", resp.StatusCode, miss.Miss)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.outcomes) != 2 || recorder.outcomes[0] != serverstore.SearchOutcomeSampleHit || recorder.outcomes[1] != serverstore.SearchOutcomeNoMatch {
		t.Fatalf("recorded outcomes = %v", recorder.outcomes)
	}
	for _, at := range recorder.times {
		if !at.Equal(testNow) {
			t.Errorf("recorded time = %s, want %s", at, testNow)
		}
	}
}

func TestInvalidSearchesAreNotOutcomeDenominator(t *testing.T) {
	var recorder *outcomeRecordingStore
	srv, _, _ := newTestServer(t, func(d *Deps) {
		recorder = &outcomeRecordingStore{Store: d.Store}
		d.Store = recorder
	})

	request, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/search", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed status = %d", resp.StatusCode)
	}
	recorder.mu.Lock()
	got := len(recorder.outcomes)
	recorder.mu.Unlock()
	if got != 0 {
		t.Fatalf("malformed request entered outcome denominator: %v", recorder.outcomes)
	}
}

func TestOutcomeWriteFailureDoesNotFailValidSearch(t *testing.T) {
	var recorder *outcomeRecordingStore
	srv, fake, _ := newTestServer(t, func(d *Deps) {
		recorder = &outcomeRecordingStore{Store: d.Store, err: errors.New("metrics unavailable")}
		d.Store = recorder
	})
	saveTestSample(t, fake, "PUBLISHED")

	var out domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v1/search", domain.SearchRequest{
		Query: "post JSON with axios", Packages: []string{"pkg:npm/axios@1.12.0"},
		Symbols: []string{"axios.post"}, Environment: nodeEnv("esm"),
	}, &out)
	if resp.StatusCode != http.StatusOK || out.Miss {
		t.Fatalf("analytics failure changed search = status:%d miss:%v", resp.StatusCode, out.Miss)
	}
}
