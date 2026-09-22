package httpapi

// The Go runtime's own account of this process, appended to GET
// /v1/ops/pool-metrics (#485).
//
// The 2026-09-17..19 verifier outage was read backwards for two days because
// every operator surface stopped one layer above the cause. The pool said
// "farm_ingest limit 0", the governor said "host-cpu-steal", the runbook said
// "resize the instance" -- and nothing anywhere could say that csx-server's
// anonymous memory (617 MiB resident + 199 MiB swapped) was already past its
// 600 MiB GOMEMLIMIT, which is the state in which the collector runs at its
// 50% CPU cap on every cycle, burns the burstable baseline of a 2-vCPU
// instance, and makes the steal the governor was reacting to. That reading
// took a host shell, /proc and a cgroup memory.stat; it belongs beside the
// pool counters an operator already polls.
//
// The fields are the runtime/metrics samples that decide the question and no
// more. gcCPUFraction is the cumulative share of this process's CPU time the
// collector took since boot; gcLimiterLastEnabledCycle against gcCycles says
// whether the memory-limit CPU limiter is engaged RIGHT NOW (equal, or within
// a few cycles: yes); memoryTotalBytes against memoryLimitBytes is the
// comparison that makes it engage.

import (
	"math"
	"runtime/metrics"
)

// opsRuntime is the wire shape. Every field is a direct runtime/metrics
// sample; the one derived number says what it is derived from.
type opsRuntime struct {
	// GoMaxProcs is the runtime's parallelism, which is also the divisor of
	// the GC CPU limiter's 50% cap.
	GoMaxProcs int64 `json:"goMaxProcs"`
	Goroutines int64 `json:"goroutines"`
	// MemoryLimitBytes is GOMEMLIMIT as the runtime holds it; math.MaxInt64
	// (no limit) is reported as 0.
	MemoryLimitBytes uint64 `json:"memoryLimitBytes"`
	// MemoryTotalBytes is /memory/classes/total:bytes -- all memory the Go
	// runtime has mapped and not released, the figure GOMEMLIMIT is compared
	// against.
	MemoryTotalBytes uint64 `json:"memoryTotalBytes"`
	// HeapLiveBytes is the heap that survived the last collection;
	// HeapGoalBytes is the size the next collection is aiming at. A goal
	// pinned at the limit with live close behind it is the thrash state.
	HeapLiveBytes uint64 `json:"heapLiveBytes"`
	HeapGoalBytes uint64 `json:"heapGoalBytes"`
	// GCCycles is the total number of completed collections since boot.
	GCCycles uint64 `json:"gcCycles"`
	// GCLimiterLastEnabledCycle is the cycle the GC CPU limiter was last
	// engaged on. 0 means never. Compared with GCCycles it says whether the
	// process is being limited now.
	GCLimiterLastEnabledCycle uint64 `json:"gcLimiterLastEnabledCycle"`
	// GCCPUSeconds and TotalCPUSeconds are the runtime's CPU accounting since
	// boot; GCCPUFraction is their ratio, 0 when nothing was measured.
	GCCPUSeconds    float64 `json:"gcCPUSeconds"`
	TotalCPUSeconds float64 `json:"totalCPUSeconds"`
	GCCPUFraction   float64 `json:"gcCPUFraction"`
}

// runtimeMetricNames are the samples read, in one runtime/metrics.Read call.
// Names are the Go 1.21+ set; one the running runtime does not know comes
// back KindBad and is reported as 0 rather than failing the whole response.
var runtimeMetricNames = []string{
	"/sched/gomaxprocs:threads",
	"/sched/goroutines:goroutines",
	"/gc/gomemlimit:bytes",
	"/memory/classes/total:bytes",
	"/gc/heap/live:bytes",
	"/gc/heap/goal:bytes",
	"/gc/cycles/total:gc-cycles",
	"/gc/limiter/last-enabled:gc-cycle",
	"/cpu/classes/gc/total:cpu-seconds",
	"/cpu/classes/total:cpu-seconds",
}

// runtimeSample is one metric as a plain number, so the mapping below can be
// tested with values the test chooses: metrics.Value has no constructor, and
// the only one a test can obtain is whatever this test process's collector
// happens to be doing.
type runtimeSample struct {
	u64 uint64
	f64 float64
	ok  bool // false: KindBad, the running runtime does not export this name
}

// readOpsRuntime samples the runtime. It is a single metrics.Read, which the
// runtime documents as cheap enough to call on every request; it takes no
// lock the collector contends for.
func readOpsRuntime() opsRuntime {
	samples := make([]metrics.Sample, len(runtimeMetricNames))
	for i, name := range runtimeMetricNames {
		samples[i].Name = name
	}
	metrics.Read(samples)
	values := make(map[string]runtimeSample, len(samples))
	for _, s := range samples {
		switch s.Value.Kind() {
		case metrics.KindUint64:
			values[s.Name] = runtimeSample{u64: s.Value.Uint64(), ok: true}
		case metrics.KindFloat64:
			values[s.Name] = runtimeSample{f64: s.Value.Float64(), ok: true}
		}
	}
	return runtimeFromValues(values)
}

// runtimeFromValues is the pure half of readOpsRuntime.
func runtimeFromValues(values map[string]runtimeSample) opsRuntime {
	var out opsRuntime
	out.GoMaxProcs = int64(values["/sched/gomaxprocs:threads"].u64)
	out.Goroutines = int64(values["/sched/goroutines:goroutines"].u64)
	// The runtime spells "no limit" as MaxInt64; on the wire that number
	// reads as a limit of eight exbibytes, so it is 0 here.
	if limit := values["/gc/gomemlimit:bytes"]; limit.ok && limit.u64 < math.MaxInt64 {
		out.MemoryLimitBytes = limit.u64
	}
	out.MemoryTotalBytes = values["/memory/classes/total:bytes"].u64
	out.HeapLiveBytes = values["/gc/heap/live:bytes"].u64
	out.HeapGoalBytes = values["/gc/heap/goal:bytes"].u64
	out.GCCycles = values["/gc/cycles/total:gc-cycles"].u64
	out.GCLimiterLastEnabledCycle = values["/gc/limiter/last-enabled:gc-cycle"].u64
	out.GCCPUSeconds = values["/cpu/classes/gc/total:cpu-seconds"].f64
	out.TotalCPUSeconds = values["/cpu/classes/total:cpu-seconds"].f64
	if out.TotalCPUSeconds > 0 {
		out.GCCPUFraction = out.GCCPUSeconds / out.TotalCPUSeconds
	}
	return out
}
