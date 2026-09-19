package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The runtime section is on the wire with every field an operator reads to
// decide "is the memory-limit GC limiter the reason for the steal": the
// limit, what is mapped against it, live/goal heap, the cycle counter and the
// limiter's last cycle, and the GC share of CPU. It comes from the process
// itself, so it needs no dependency and cannot be "not configured".
func TestOpsMetricsHandlerReportsTheRuntimeSection(t *testing.T) {
	h := &OpsMetricsHandler{}
	req := httptest.NewRequest(http.MethodGet, "/v1/ops/pool-metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	rt, ok := got["runtime"].(map[string]any)
	if !ok {
		t.Fatalf("response missing object field \"runtime\": %s", rec.Body.String())
	}
	for _, field := range []string{
		"goMaxProcs", "goroutines", "memoryLimitBytes", "memoryTotalBytes",
		"heapLiveBytes", "heapGoalBytes", "gcCycles", "gcLimiterLastEnabledCycle",
		"gcCPUSeconds", "totalCPUSeconds", "gcCPUFraction",
	} {
		if _, present := rt[field]; !present {
			t.Fatalf("runtime missing field %q: %#v", field, rt)
		}
	}
	// A live process always has these: at least one P, at least this
	// goroutine, and some mapped memory. They prove the samples are read from
	// the runtime rather than left at their zero values.
	if rt["goMaxProcs"].(float64) < 1 || rt["goroutines"].(float64) < 1 || rt["memoryTotalBytes"].(float64) <= 0 {
		t.Fatalf("runtime section reads as unsampled: %#v", rt)
	}
}

// The pure half, fed the shapes the runtime produces: an unset GOMEMLIMIT is
// MaxInt64 in the runtime and 0 on the wire, a metric this runtime does not
// export is 0 rather than a failed response, and the GC CPU fraction is the
// ratio of the two cpu-seconds samples.
func TestRuntimeFromValuesMapsEachMetric(t *testing.T) {
	u := func(v uint64) runtimeSample { return runtimeSample{u64: v, ok: true} }
	f := func(v float64) runtimeSample { return runtimeSample{f64: v, ok: true} }
	got := runtimeFromValues(map[string]runtimeSample{
		"/sched/gomaxprocs:threads":         u(2),
		"/sched/goroutines:goroutines":      u(37),
		"/gc/gomemlimit:bytes":              u(600 << 20),
		"/memory/classes/total:bytes":       u(816 << 20),
		"/gc/heap/live:bytes":               u(590 << 20),
		"/gc/heap/goal:bytes":               u(600 << 20),
		"/gc/cycles/total:gc-cycles":        u(41203),
		"/gc/limiter/last-enabled:gc-cycle": u(41202),
		"/cpu/classes/gc/total:cpu-seconds": f(90000),
		"/cpu/classes/total:cpu-seconds":    f(180000),
	})
	if got.GoMaxProcs != 2 || got.Goroutines != 37 {
		t.Fatalf("sched = %+v", got)
	}
	if got.MemoryLimitBytes != 600<<20 || got.MemoryTotalBytes != 816<<20 {
		t.Fatalf("memory = %+v", got)
	}
	if got.HeapLiveBytes != 590<<20 || got.HeapGoalBytes != 600<<20 {
		t.Fatalf("heap = %+v", got)
	}
	if got.GCCycles != 41203 || got.GCLimiterLastEnabledCycle != 41202 {
		t.Fatalf("gc cycles = %+v", got)
	}
	if got.GCCPUFraction != 0.5 {
		t.Fatalf("GCCPUFraction = %v, want 0.5", got.GCCPUFraction)
	}

	unlimited := runtimeFromValues(map[string]runtimeSample{"/gc/gomemlimit:bytes": u(math.MaxInt64)})
	if unlimited.MemoryLimitBytes != 0 {
		t.Fatalf("no GOMEMLIMIT must be 0 on the wire, got %d", unlimited.MemoryLimitBytes)
	}
	if unlimited.GCCPUFraction != 0 || unlimited.GCCycles != 0 {
		t.Fatalf("absent samples must be zero, got %+v", unlimited)
	}
}
