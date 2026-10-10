package main

import (
	"io"
	"math"
	"runtime"
	"strings"
	"time"
)

// An incident diagnostic in the existing stdout log: no profile endpoint,
// stack traces, paths, package coordinates or changes to GC/memory policy.
// A bounded sample can be up to two GC cycles old; its byte counts are
// probability-corrected estimates, not an exact retained-heap census.
const (
	heapAllocationThreshold  = 500 << 20
	heapAllocationInterval   = 5 * time.Minute
	heapAllocationMaxRecords = 4096
)

var heapAllocationKinds = [...]string{
	"snapshot_corpus", "snapshot_payload", "builder_evidence",
	"builder_receipts", "builder_calculation", "builder_other",
	"web_cache", "http_api", "database_driver", "runtime", "other",
}

type heapAllocationBucket struct {
	Kind                string `json:"kind"`
	SampledInUseBytes   uint64 `json:"sampled_inuse_bytes"`
	EstimatedInUseBytes uint64 `json:"estimated_inuse_bytes"`
	Records             int    `json:"records"`
}

type heapAllocationRecord struct {
	Event               string                 `json:"event"`
	Time                string                 `json:"time"`
	State               string                 `json:"state"`
	Reason              string                 `json:"reason,omitempty"`
	SampleRateBytes     int                    `json:"sample_rate_bytes"`
	MaxGCAgeCycles      int                    `json:"max_gc_age_cycles"`
	RecordLimit         int                    `json:"record_limit"`
	Records             int                    `json:"records"`
	EstimatedInUseBytes *uint64                `json:"estimated_inuse_bytes,omitempty"`
	Buckets             []heapAllocationBucket `json:"buckets,omitempty"`
}

type heapAllocationCadence struct{ last time.Time }

func (c *heapAllocationCadence) due(now time.Time, heap uint64) bool {
	if heap < heapAllocationThreshold || (!c.last.IsZero() && now.Sub(c.last) < heapAllocationInterval) {
		return false
	}
	c.last = now
	return true
}

func emitHeapAllocation(dst io.Writer, at time.Time) {
	record := readHeapAllocation(runtime.MemProfile, runtime.MemProfileRate, heapAllocationKind)
	record.Event = "heap_allocation_estimate"
	record.Time = at.UTC().Format(time.RFC3339Nano)
	writeObservation(dst, record)
}

func readHeapAllocation(profile func([]runtime.MemProfileRecord, bool) (int, bool), rate int, classify func([]uintptr) string) heapAllocationRecord {
	out := heapAllocationRecord{State: "unavailable", SampleRateBytes: rate, MaxGCAgeCycles: 2, RecordLimit: heapAllocationMaxRecords}
	if rate <= 0 {
		out.Reason = "profile-disabled"
		return out
	}
	records := make([]runtime.MemProfileRecord, heapAllocationMaxRecords)
	n, ok := profile(records, false)
	out.Records = n
	if !ok || n < 0 || n > len(records) {
		out.Reason = "record-budget"
		return out
	}
	buckets := make([]heapAllocationBucket, len(heapAllocationKinds))
	for i, kind := range heapAllocationKinds {
		buckets[i].Kind = kind
	}
	var total uint64
	for i := 0; i < n; i++ {
		r := &records[i]
		sampled, objects := r.InUseBytes(), r.InUseObjects()
		estimate, valid := estimateHeapBytes(sampled, objects, rate)
		if !valid {
			out.Reason = "invalid-sample"
			return out
		}
		if sampled == 0 {
			continue
		}
		kind := classify(r.Stack())
		index := len(heapAllocationKinds) - 1
		for j, known := range heapAllocationKinds {
			if known == kind {
				index = j
				break
			}
		}
		b := &buckets[index]
		if ^uint64(0)-total < estimate || ^uint64(0)-b.SampledInUseBytes < uint64(sampled) {
			out.Reason = "invalid-sample"
			return out
		}
		total += estimate
		b.EstimatedInUseBytes += estimate
		b.SampledInUseBytes += uint64(sampled)
		b.Records++
	}
	out.State = "estimated"
	out.EstimatedInUseBytes = &total
	for _, bucket := range buckets {
		if bucket.Records > 0 {
			out.Buckets = append(out.Buckets, bucket)
		}
	}
	return out
}

// Same Poisson probability correction as the Go heap profiler. Expm1 keeps
// small allocation sizes numerically stable. Do not change MemProfileRate.
func estimateHeapBytes(bytes, objects int64, rate int) (uint64, bool) {
	if bytes < 0 || objects < 0 || (bytes == 0) != (objects == 0) {
		return 0, false
	}
	if bytes == 0 {
		return 0, true
	}
	if rate <= 0 {
		return 0, false
	}
	if rate == 1 {
		return uint64(bytes), true
	}
	probability := -math.Expm1(-float64(bytes) / float64(objects) / float64(rate))
	estimate := float64(bytes) / probability
	if math.IsNaN(estimate) || math.IsInf(estimate, 0) || estimate < 0 || estimate >= float64(1<<63) {
		return 0, false
	}
	return uint64(estimate), true
}

func heapAllocationKind(pcs []uintptr) string {
	best := len(heapAllocationKinds) - 1
	frames := runtime.CallersFrames(pcs)
	for {
		frame, more := frames.Next()
		index := heapAllocationFunctionKind(frame.Function)
		if index < best {
			best = index
		}
		if !more {
			break
		}
	}
	return heapAllocationKinds[best]
}

// Function names are consumed locally and never copied into log records.
// Output is always one member of the fixed enum, including unknown stacks.
func heapAllocationFunctionKind(name string) int {
	const project = "github.com/r2cuerdame/codesamplex/"
	switch {
	case strings.HasPrefix(name, project+"internal/serverstore.(*PG).ListSnapshots"):
		return 0
	case strings.HasPrefix(name, project+"internal/serverstore.(*PG).GetSnapshot"),
		strings.HasPrefix(name, project+"internal/serverstore.(*PG).SnapshotsForPURLs"):
		return 1
	case strings.HasPrefix(name, project+"internal/serverstore.(*PG).EvidenceFor"):
		return 2
	case strings.HasPrefix(name, project+"internal/serverstore.(*PG).ReceiptsFor"),
		strings.HasPrefix(name, project+"internal/compatibility.(*Builder).collectSamples"),
		strings.HasPrefix(name, project+"internal/compatibility.(*Builder).loadSamples"):
		return 3
	case strings.HasPrefix(name, project+"internal/compatibility.Build"),
		strings.HasPrefix(name, project+"internal/compatibility.Compute"):
		return 4
	case strings.HasPrefix(name, project+"internal/compatibility."):
		return 5
	case strings.HasPrefix(name, "main.(*webStore)."):
		return 6
	case strings.HasPrefix(name, project+"internal/httpapi."):
		return 7
	case strings.HasPrefix(name, "github.com/jackc/"):
		return 8
	case strings.HasPrefix(name, "runtime."), strings.HasPrefix(name, "internal/runtime/"):
		return 9
	default:
		return 10
	}
}
