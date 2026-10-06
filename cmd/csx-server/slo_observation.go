package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// sloObserver writes bounded route names and runtime samples to the ordinary
// server log. Both events use UTC completion/sample timestamps, so operators
// can join them to the existing host steal observations without a new probe.
type sloObserver struct {
	logger *log.Logger
}

func newSLOObserver(logger *log.Logger) *sloObserver {
	return &sloObserver{logger: logger}
}

func defaultSLOObserver() *sloObserver {
	// A dedicated logger keeps each record one JSON object per line; the
	// ordinary logger's human timestamp prefix would break JSON consumers.
	return newSLOObserver(log.New(log.Writer(), "", 0))
}

func (o *sloObserver) write(value any) {
	line, err := json.Marshal(value)
	if err == nil {
		o.logger.Print(string(line))
	}
}

func observeSLORequests(mux *http.ServeMux, observer *sloObserver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pattern := mux.Handler(r)
		route := pattern
		if route == "" {
			route = "unmatched"
		}
		// ServeMux's wildcard covers an arbitrary package path, including
		// scoped names. Never put those path values into the operator log.
		if pattern == "GET /v1/shards/{ecosystem}/{rest...}" {
			route = "GET /v1/shards/:ecosystem/:package/:major"
		}
		start := time.Now()
		defer func() {
			observer.write(struct {
				Event      string  `json:"event"`
				At         string  `json:"at"`
				Route      string  `json:"route"`
				DurationMS float64 `json:"duration_ms"`
			}{
				Event:      "slo_request",
				At:         time.Now().UTC().Format(time.RFC3339Nano),
				Route:      route,
				DurationMS: float64(time.Since(start)) / float64(time.Millisecond),
			})
		}()
		mux.ServeHTTP(w, r)
	})
}

func (o *sloObserver) writeRuntimeSample(readRSS func() (uint64, error)) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	rss, err := readRSS()
	var rssBytes *uint64
	if err == nil {
		rssBytes = &rss
	}
	o.write(struct {
		Event           string  `json:"event"`
		At              string  `json:"at"`
		HeapAllocBytes  uint64  `json:"heap_alloc_bytes"`
		HeapInuseBytes  uint64  `json:"heap_inuse_bytes"`
		NumGC           uint32  `json:"num_gc"`
		GCPauseTotalMS  float64 `json:"gc_pause_total_ms"`
		ProcessRSSBytes *uint64 `json:"process_rss_bytes"`
	}{
		Event:           "slo_runtime",
		At:              time.Now().UTC().Format(time.RFC3339Nano),
		HeapAllocBytes:  stats.HeapAlloc,
		HeapInuseBytes:  stats.HeapInuse,
		NumGC:           stats.NumGC,
		GCPauseTotalMS:  float64(stats.PauseTotalNs) / float64(time.Millisecond),
		ProcessRSSBytes: rssBytes,
	})
}

func (o *sloObserver) startRuntimeSamples(ctx context.Context, interval time.Duration, readRSS func() (uint64, error)) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.writeRuntimeSample(readRSS)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.writeRuntimeSample(readRSS)
			}
		}
	}()
	return done
}

// Linux exposes resident pages without an authenticated metrics endpoint.
// Other platforms log JSON null rather than claiming an RSS of zero.
func processRSSBytes() (uint64, error) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, errors.New("statm has no resident field")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * uint64(os.Getpagesize()), nil
}
