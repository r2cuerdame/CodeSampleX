package main

import (
	"encoding/json"
	"math"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHeapAllocationCadenceRequiresPressureAndBoundsRate(t *testing.T) {
	var c heapAllocationCadence
	now := time.Unix(1000, 0)
	if c.due(now, heapAllocationThreshold-1) || !c.last.IsZero() {
		t.Fatal("low heap starts profiling")
	}
	if !c.due(now, heapAllocationThreshold) {
		t.Fatal("first pressure sample missing")
	}
	if c.due(now.Add(30*time.Second), heapAllocationThreshold) ||
		c.due(now.Add(-time.Second), heapAllocationThreshold) {
		t.Fatal("profile repeats early")
	}
	if !c.due(now.Add(heapAllocationInterval), heapAllocationThreshold) {
		t.Fatal("next bounded sample missing")
	}
}

func TestHeapAllocationCorrectionAccountsForObjectSampling(t *testing.T) {
	bytes, ok := estimateHeapBytes(64, 1, 512*1024)
	want := float64(64) / (1 - math.Exp(-64.0/(512*1024)))
	if !ok || math.Abs(float64(bytes)-want) > 1 {
		t.Fatalf("estimate=%d ok=%t want=%f", bytes, ok, want)
	}
	if got, ok := estimateHeapBytes(64, 1, 1); !ok || got != 64 {
		t.Fatal("rate one must preserve measured sample")
	}
	for _, values := range [][2]int64{{-1, 1}, {1, -1}, {1, 0}, {0, 1}} {
		if _, ok := estimateHeapBytes(values[0], values[1], 512*1024); ok {
			t.Fatalf("invalid sample accepted: %v", values)
		}
	}
	if _, ok := estimateHeapBytes(64, 1, 0); ok {
		t.Fatal("disabled sampling became an estimate")
	}
}

func TestHeapAllocationIsBoundedAndSuppressesPartialTotals(t *testing.T) {
	for _, state := range []string{"overflow", "invalid", "disabled"} {
		t.Run(state, func(t *testing.T) {
			called := false
			rate := 512 * 1024
			if state == "disabled" {
				rate = 0
			}
			out := readHeapAllocation(func(p []runtime.MemProfileRecord, zero bool) (int, bool) {
				called = true
				if len(p) != 4096 || zero {
					t.Fatal("unbounded or freed-object profile requested")
				}
				if state == "overflow" {
					return len(p) + 1, false
				}
				p[0] = runtime.MemProfileRecord{AllocBytes: 64, AllocObjects: 1}
				p[1] = runtime.MemProfileRecord{AllocBytes: 1, AllocObjects: 0}
				return 2, true
			}, rate, func([]uintptr) string { return "snapshot_corpus" })
			if out.State != "unavailable" || out.EstimatedInUseBytes != nil || len(out.Buckets) != 0 {
				t.Fatalf("partial profile escaped: %+v", out)
			}
			if state == "disabled" && called {
				t.Fatal("disabled profile was queried")
			}
		})
	}
}

func TestHeapAllocationOnlyEmitsFixedKindsAndNumericEstimates(t *testing.T) {
	out := readHeapAllocation(func(p []runtime.MemProfileRecord, _ bool) (int, bool) {
		p[0] = runtime.MemProfileRecord{AllocBytes: 64, AllocObjects: 1}
		p[1] = runtime.MemProfileRecord{AllocBytes: 128, AllocObjects: 1}
		return 2, true
	}, 1, func([]uintptr) string { return "private/C:/user/token/sample-coordinate" })
	if out.State != "estimated" || out.EstimatedInUseBytes == nil || *out.EstimatedInUseBytes != 192 ||
		len(out.Buckets) != 1 || out.Buckets[0].Kind != "other" || out.Buckets[0].SampledInUseBytes != 192 {
		t.Fatalf("unsafe or wrong estimate: %+v", out)
	}
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "private") || strings.Contains(string(b), "token") || strings.Contains(string(b), "coordinate") {
		t.Fatalf("private classifier data logged: %s", b)
	}
	const prefix = "github.com/r2cuerdame/codesamplex/"
	if heapAllocationFunctionKind(prefix+"internal/serverstore.(*PG).ListSnapshots") != 0 ||
		heapAllocationFunctionKind(prefix+"internal/serverstore.(*PG).EvidenceForTargets") != 2 ||
		heapAllocationFunctionKind(prefix+"internal/compatibility.BuildClusters") != 4 ||
		heapAllocationFunctionKind("unknown-private-function") != 10 {
		t.Fatal("known callsite classification missing")
	}
}
