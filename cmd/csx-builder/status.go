package main

// The standalone Builder's own health/ready/progress surface (CSX-451).
// csx-server's public routes never run inside this process, so this file
// has no reader-facing content at all -- only what ops/observability polls.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// passTracker is Builder.OnPass's target: the counters and timestamps
// /progress reports. A separate small type rather than fields on Builder
// itself, because Builder already documents its own fields as owned by
// RunLoop's single goroutine (see builder.go) and this is read from an HTTP
// handler goroutine concurrently.
type passTracker struct {
	mu         sync.Mutex
	passes     int64
	failures   int64
	lastStart  time.Time
	lastFinish time.Time
	lastErr    string
}

func (t *passTracker) record(err error, startedAt, finishedAt time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.passes++
	t.lastStart = startedAt
	t.lastFinish = finishedAt
	if err != nil {
		t.failures++
		t.lastErr = err.Error()
	} else {
		t.lastErr = ""
	}
}

func (t *passTracker) snapshot() passStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return passStatus{
		Passes:     t.passes,
		Failures:   t.failures,
		LastStart:  t.lastStart,
		LastFinish: t.lastFinish,
		LastError:  t.lastErr,
	}
}

type passStatus struct {
	Passes     int64
	Failures   int64
	LastStart  time.Time
	LastFinish time.Time
	LastError  string
}

// healthzTimeout mirrors csx-server's own (internal/httpapi/api.go): a
// probe that cannot get a trivial answer inside a few seconds is itself the
// unhealthy signal, and must not hang the caller waiting on one.
const healthzTimeout = 3 * time.Second

// databaseReachable runs one cheap ClassProbe-budgeted read, the same
// admission class csx-server's own /healthz uses, so a Builder pass that
// has the rest of its (small) pool busy cannot make this check queue behind
// it -- ProbeReserve exists in BuilderPoolPolicy for exactly this endpoint.
func databaseReachable(ctx context.Context, store serverstore.Store, leaseName string) error {
	ctx, cancel := context.WithTimeout(ctx, healthzTimeout)
	defer cancel()
	ctx = serverstore.WithQueryClass(ctx, serverstore.ClassProbe)
	_, _, err := store.GetBuilderLease(ctx, leaseName)
	return err
}

func newStatusMux(store serverstore.Store, leader *compatibility.Leader, tracker *passTracker, leaseName string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := databaseReachable(r.Context(), store, leaseName); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	// Distinct from /healthz today only in name: an orchestrator that wires
	// liveness to one and readiness to the other must still see a real
	// answer from both. A future split (e.g. "ready" excluding a process
	// that has never yet acquired the lease) can change this independently
	// of /healthz's contract.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := databaseReachable(r.Context(), store, leaseName); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("database unavailable"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /progress", func(w http.ResponseWriter, r *http.Request) {
		writeProgress(w, leader.Status(), tracker.snapshot())
	})
	return mux
}

func writeProgress(w http.ResponseWriter, lease compatibility.LeaderStatus, pass passStatus) {
	type leaseJSON struct {
		Held bool `json:"held"`
		// Paused is what the pass loop's last pause poll read (#454): true
		// means csx-server's resource governor is currently shedding this
		// pipeline, and the passes counter standing still is expected
		// rather than a symptom.
		Paused      bool    `json:"paused"`
		Owner       string  `json:"owner,omitempty"`
		Fence       int64   `json:"fence,omitempty"`
		ExpiresAt   *string `json:"expiresAt,omitempty"`
		LastAttempt *string `json:"lastAttempt,omitempty"`
		LastError   string  `json:"lastError,omitempty"`
	}
	type passJSON struct {
		Passes     int64   `json:"passes"`
		Failures   int64   `json:"failures"`
		LastStart  *string `json:"lastStart,omitempty"`
		LastFinish *string `json:"lastFinish,omitempty"`
		LastError  string  `json:"lastError,omitempty"`
	}
	rfc3339 := func(t time.Time) *string {
		if t.IsZero() {
			return nil
		}
		s := t.UTC().Format(time.RFC3339)
		return &s
	}
	body := struct {
		Lease leaseJSON `json:"lease"`
		Pass  passJSON  `json:"pass"`
	}{
		Lease: leaseJSON{
			Held:        lease.Held,
			Paused:      lease.Paused,
			Owner:       lease.Lease.Owner,
			Fence:       lease.Lease.Fence,
			ExpiresAt:   rfc3339(lease.Lease.ExpiresAt),
			LastAttempt: rfc3339(lease.LastAttempt),
			LastError:   lease.LastError,
		},
		Pass: passJSON{
			Passes:     pass.Passes,
			Failures:   pass.Failures,
			LastStart:  rfc3339(pass.LastStart),
			LastFinish: rfc3339(pass.LastFinish),
			LastError:  pass.LastError,
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(body)
}
