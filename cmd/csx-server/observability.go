package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"time"
)

// Keep request and runtime records in the server container's existing stdout
// log. Routes are ServeMux patterns, never raw paths, query strings or IDs.
const runtimeSnapshotInterval = 30 * time.Second

type requestRecord struct {
	Event      string  `json:"event"`
	Time       string  `json:"time"`
	Route      string  `json:"route"`
	Status     int     `json:"status"`
	DurationMS float64 `json:"duration_ms"`
}

type runtimeRecord struct {
	Event          string  `json:"event"`
	Time           string  `json:"time"`
	GoMaxProcs     int     `json:"go_max_procs"`
	HeapAllocBytes uint64  `json:"heap_alloc_bytes"`
	HeapInuseBytes uint64  `json:"heap_inuse_bytes"`
	GCCount        uint32  `json:"gc_count"`
	GCPauseTotalNS uint64  `json:"gc_pause_total_ns"`
	GCLastPauseNS  uint64  `json:"gc_last_pause_ns"`
	RSSBytes       *uint64 `json:"rss_bytes"`
}

func writeObservation(dst io.Writer, record any) {
	line, err := json.Marshal(record)
	if err == nil {
		_, _ = dst.Write(append(line, '\n'))
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.status == 0 {
		w.status = code
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func requestObservation(routes *http.ServeMux, next http.Handler, dst io.Writer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, route := routes.Handler(r)
		if route == "" {
			route = "unmatched"
		}
		started := time.Now()
		tracked := &statusWriter{ResponseWriter: w}
		defer func() {
			status := tracked.status
			if status == 0 {
				status = http.StatusOK
			}
			writeObservation(dst, requestRecord{
				Event: "http_request", Time: time.Now().UTC().Format(time.RFC3339Nano),
				Route: route, Status: status,
				DurationMS: float64(time.Since(started)) / float64(time.Millisecond),
			})
		}()
		next.ServeHTTP(tracked, r)
	})
}

func readRuntimeSnapshot() runtimeRecord {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	record := runtimeRecord{
		GoMaxProcs:     runtime.GOMAXPROCS(0),
		HeapAllocBytes: mem.HeapAlloc, HeapInuseBytes: mem.HeapInuse,
		GCCount: mem.NumGC, GCPauseTotalNS: mem.PauseTotalNs,
	}
	if mem.NumGC > 0 {
		record.GCLastPauseNS = mem.PauseNs[(mem.NumGC-1)%uint32(len(mem.PauseNs))]
	}
	// Production runs on Linux. A missing procfs is distinguishable from zero
	// RSS, which otherwise looks like a very healthy process.
	if f, err := os.Open("/proc/self/statm"); err == nil {
		var size, resident uint64
		if _, err := fmt.Fscan(f, &size, &resident); err == nil {
			bytes := resident * uint64(os.Getpagesize())
			record.RSSBytes = &bytes
		}
		_ = f.Close()
	}
	return record
}

func logRuntimeSnapshots(ctx context.Context, dst io.Writer, interval time.Duration, now func() time.Time, sample func() runtimeRecord) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitRuntimeSnapshots(ctx, dst, ticker.C, now, sample)
}

func emitRuntimeSnapshots(ctx context.Context, dst io.Writer, ticks <-chan time.Time, now func() time.Time, sample func() runtimeRecord) {
	var allocationCadence heapAllocationCadence
	for {
		record := sample()
		record.Event = "go_runtime"
		at := now().UTC()
		record.Time = at.Format(time.RFC3339Nano)
		writeObservation(dst, record)
		if allocationCadence.due(at, record.HeapAllocBytes) {
			emitHeapAllocation(dst, at)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}
