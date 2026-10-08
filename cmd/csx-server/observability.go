package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/metrics"
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
	Event                     string  `json:"event"`
	Time                      string  `json:"time"`
	GoMaxProcs                int     `json:"go_max_procs"`
	HeapAllocBytes            uint64  `json:"heap_alloc_bytes"`
	HeapInuseBytes            uint64  `json:"heap_inuse_bytes"`
	GCCount                   uint32  `json:"gc_count"`
	GCPauseTotalNS            uint64  `json:"gc_pause_total_ns"`
	GCLastPauseNS             uint64  `json:"gc_last_pause_ns"`
	Goroutines                uint64  `json:"goroutines"`
	MemoryLimitBytes          uint64  `json:"memory_limit_bytes"`
	MemoryTotalBytes          uint64  `json:"memory_total_bytes"`
	HeapLiveBytes             uint64  `json:"heap_live_bytes"`
	HeapGoalBytes             uint64  `json:"heap_goal_bytes"`
	GCLimiterLastEnabledCycle uint64  `json:"gc_limiter_last_enabled_cycle"`
	GCCPUSeconds              float64 `json:"gc_cpu_seconds"`
	TotalCPUSeconds           float64 `json:"total_cpu_seconds"`
	RSSBytes                  *uint64 `json:"rss_bytes"`
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
	// These cumulative counters make consecutive log samples comparable without
	// requiring an operator token for the protected ops endpoint.
	samples := []metrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/gc/gomemlimit:bytes"},
		{Name: "/memory/classes/total:bytes"},
		{Name: "/gc/heap/live:bytes"},
		{Name: "/gc/heap/goal:bytes"},
		{Name: "/gc/limiter/last-enabled:gc-cycle"},
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/cpu/classes/total:cpu-seconds"},
	}
	metrics.Read(samples)
	record.Goroutines = metricUint64(samples[0])
	// The runtime represents an unlimited memory budget as MaxInt64.
	if limit := metricUint64(samples[1]); limit < uint64(1<<63-1) {
		record.MemoryLimitBytes = limit
	}
	record.MemoryTotalBytes = metricUint64(samples[2])
	record.HeapLiveBytes = metricUint64(samples[3])
	record.HeapGoalBytes = metricUint64(samples[4])
	record.GCLimiterLastEnabledCycle = metricUint64(samples[5])
	record.GCCPUSeconds = metricFloat64(samples[6])
	record.TotalCPUSeconds = metricFloat64(samples[7])
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

func metricUint64(sample metrics.Sample) uint64 {
	if sample.Value.Kind() == metrics.KindUint64 {
		return sample.Value.Uint64()
	}
	return 0
}

func metricFloat64(sample metrics.Sample) float64 {
	if sample.Value.Kind() == metrics.KindFloat64 {
		return sample.Value.Float64()
	}
	return 0
}

func logRuntimeSnapshots(ctx context.Context, dst io.Writer, interval time.Duration, now func() time.Time, sample func() runtimeRecord) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	emitRuntimeSnapshots(ctx, dst, ticker.C, now, sample)
}

func emitRuntimeSnapshots(ctx context.Context, dst io.Writer, ticks <-chan time.Time, now func() time.Time, sample func() runtimeRecord) {
	for {
		record := sample()
		record.Event = "go_runtime"
		record.Time = now().UTC().Format(time.RFC3339Nano)
		writeObservation(dst, record)
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
	}
}
