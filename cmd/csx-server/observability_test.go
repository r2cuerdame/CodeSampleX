package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
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

func TestRuntimeSnapshotReportsAppliedGoMaxProcs(t *testing.T) {
	if got, want := readRuntimeSnapshot().GoMaxProcs, runtime.GOMAXPROCS(0); got != want {
		t.Fatalf("reported GOMAXPROCS = %d, runtime = %d", got, want)
	}
}

type blockingObservationWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingObservationWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.release:
	case <-time.After(5 * time.Second):
	}
	return len(p), nil
}

// #716: verify the full csx-server wrapper chain (requestObservation +
// withDBBudget -> httpapi.NewMux) flushes complete timed responses before the
// post-handler observation write returns.
func TestBuildMuxObservedFlushesTimedRoutesBeforeObservationWriteFinishes(t *testing.T) {
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	for _, path := range []string{"/version", "/v1/stats"} {
		t.Run(path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			obs := &blockingObservationWriter{
				started: make(chan struct{}),
				release: make(chan struct{}),
			}
			mux, _, _ := buildMuxObserved(ctx, serverstore.ServerConfig{}, serverstore.NewFake(), nil, obs)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			defer close(obs.release)

			done := make(chan error, 1)
			var resp *http.Response
			go func() {
				var err error
				resp, err = client.Get(srv.URL + path)
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: response blocked until observation write returned", path)
			}
			defer resp.Body.Close()

			select {
			case <-obs.started:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s: observation write never started", path)
			}

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusOK || len(resp.TransferEncoding) != 0 {
				t.Fatalf("%s: status %d, transfer-encoding %v; want unchunked 200", path, resp.StatusCode, resp.TransferEncoding)
			}
			if resp.ContentLength != int64(len(body)) {
				t.Fatalf("%s: Content-Length %d, body %d bytes", path, resp.ContentLength, len(body))
			}
			if resp.Header.Get("Server-Timing") == "" {
				t.Fatalf("%s: missing Server-Timing header", path)
			}
		})
	}
}
