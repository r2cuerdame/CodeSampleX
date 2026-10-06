package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The old mux serves these requests but emits no low-cardinality observations.
func TestSLORequestObservationWired(t *testing.T) {
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	defer log.SetOutput(old)
	mux := BuildMux(serverstore.ServerConfig{}, serverstore.NewFake())
	for _, path := range []string{"/healthz", "/version", "/v1/stats", "/v1/shards/npm/private-package/3"} {
		mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path+"?token=secret", nil))
	}
	var routes []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if !strings.Contains(line, `"event":"slo_request"`) {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			t.Fatalf("request observation is not a JSON line: %q", line)
		}
		var got struct {
			Event      string  `json:"event"`
			At         string  `json:"at"`
			Route      string  `json:"route"`
			DurationMS float64 `json:"duration_ms"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if got.Event != "slo_request" || got.DurationMS < 0 {
			t.Fatalf("bad request observation: %+v", got)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.At); err != nil {
			t.Fatal(err)
		}
		routes = append(routes, got.Route)
	}
	want := []string{"GET /healthz", "GET /version", "GET /v1/stats", "GET /v1/shards/:ecosystem/:package/:major"}
	if len(routes) != len(want) {
		t.Fatalf("got %d SLO request observations, want %d: %q", len(routes), len(want), out.String())
	}
	for i := range want {
		if routes[i] != want[i] {
			t.Errorf("route %d = %q, want %q", i, routes[i], want[i])
		}
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "private-package") {
		t.Fatal("observation disclosed request data")
	}
}

func TestSLORuntimeObservationFormatAndCancellation(t *testing.T) {
	var out bytes.Buffer
	observer := newSLOObserver(log.New(&out, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	done := observer.startRuntimeSamples(ctx, time.Millisecond, func() (uint64, error) { return 12345, nil })
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runtime sampler did not stop on cancellation")
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d runtime samples, want initial and periodic", len(lines))
	}
	for _, line := range lines {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"event", "at", "heap_alloc_bytes", "heap_inuse_bytes", "num_gc", "gc_pause_total_ms", "process_rss_bytes"} {
			if _, ok := fields[key]; !ok {
				t.Fatalf("runtime sample missing %s: %q", key, line)
			}
		}
		var got struct {
			Event           string  `json:"event"`
			At              string  `json:"at"`
			HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
			HeapInuseBytes  uint64  `json:"heap_inuse_bytes"`
			NumGC           uint32  `json:"num_gc"`
			GCPauseTotalMS  float64 `json:"gc_pause_total_ms"`
			ProcessRSSBytes *uint64 `json:"process_rss_bytes"`
		}
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatal(err)
		}
		if got.Event != "slo_runtime" || got.HeapAllocBytes == 0 || got.HeapInuseBytes == 0 || got.ProcessRSSBytes == nil || *got.ProcessRSSBytes != 12345 {
			t.Fatalf("bad runtime sample: %+v", got)
		}
		if _, err := time.Parse(time.RFC3339Nano, got.At); err != nil {
			t.Fatal(err)
		}
	}
}
