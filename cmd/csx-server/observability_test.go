package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestRequestObservationUsesRouteTemplateAndFinalStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/shards/{ecosystem}/{rest...}", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	mux.HandleFunc("GET /early", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusAccepted)
	})
	for _, tc := range []struct {
		path, route string
		status      int
	}{
		{"/v1/shards/npm/private-id/1?token=secret", "GET /v1/shards/{ecosystem}/{rest...}", 404},
		{"/healthz", "GET /healthz", 200},
		{"/v1/stats", "GET /v1/stats", 202},
		{"/early", "GET /early", 202},
		{"/unknown/private-id", "unmatched", 404},
	} {
		var log strings.Builder
		recorder := httptest.NewRecorder()
		requestObservation(mux, mux, &log).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tc.path, nil))
		var got requestRecord
		if err := json.Unmarshal([]byte(log.String()), &got); err != nil {
			t.Fatalf("record for %s: %v", tc.path, err)
		}
		if got.Event != "http_request" || got.Route != tc.route || got.Status != tc.status || got.DurationMS < 0 {
			t.Errorf("record for %s: %+v", tc.path, got)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.Time); err != nil {
			t.Errorf("timestamp for %s: %v", tc.path, err)
		}
		if strings.Contains(log.String(), "private-id") || strings.Contains(log.String(), "secret") {
			t.Errorf("raw URL leaked: %s", log.String())
		}
	}
}

func TestObservedServerMuxLogsMatchedRoute(t *testing.T) {
	var lines strings.Builder
	mux, _, _ := buildMuxObserved(context.Background(), serverstore.ServerConfig{}, serverstore.NewFake(), nil, &lines)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/shards/npm/secret-name/1", nil))
	var got requestRecord
	if err := json.Unmarshal([]byte(lines.String()), &got); err != nil {
		t.Fatal(err)
	}
	if got.Route != "GET /v1/shards/{ecosystem}/{rest...}" || got.Status != response.Code {
		t.Fatalf("observation = %+v; response = %d", got, response.Code)
	}
	if strings.Contains(lines.String(), "secret-name") {
		t.Fatalf("path leaked: %s", lines.String())
	}
}

type lineSink chan string

func (s lineSink) Write(p []byte) (int, error) {
	s <- string(p)
	return len(p), nil
}

func TestRuntimeSnapshotsEmitOnStartupAndEachTick(t *testing.T) {
	if runtimeSnapshotInterval != 30*time.Second {
		t.Fatalf("snapshot interval = %s", runtimeSnapshotInterval)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	lines := make(lineSink)
	done := make(chan struct{})
	now := time.Date(2026, 10, 6, 1, 2, 3, 0, time.UTC)
	go func() {
		defer close(done)
		emitRuntimeSnapshots(ctx, lines, ticks, func() time.Time { return now }, func() runtimeRecord {
			rss := uint64(789)
			return runtimeRecord{HeapAllocBytes: 123, HeapInuseBytes: 456, GCCount: 2, GCPauseTotalNS: 34, GCLastPauseNS: 12, RSSBytes: &rss}
		})
	}()
	check := func(line string) {
		t.Helper()
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		for field, want := range map[string]any{
			"event": "go_runtime", "time": now.Format(time.RFC3339Nano),
			"heap_alloc_bytes": float64(123), "heap_inuse_bytes": float64(456),
			"gc_count": float64(2), "gc_pause_total_ns": float64(34),
			"gc_last_pause_ns": float64(12), "rss_bytes": float64(789),
		} {
			if got[field] != want {
				t.Errorf("%s = %v, want %v", field, got[field], want)
			}
		}
	}
	check(<-lines)
	select {
	case line := <-lines:
		t.Fatalf("snapshot before tick: %s", line)
	default:
	}
	ticks <- now.Add(runtimeSnapshotInterval)
	check(<-lines)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("snapshot loop did not stop")
	}
}
