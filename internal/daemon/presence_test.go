package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
)

func TestPresenceDailySuppression(t *testing.T) {
	var callCount atomic.Int32
	var receivedPayload domain.PresencePayload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/presence" {
			callCount.Add(1)
			if err := json.NewDecoder(r.Body).Decode(&receivedPayload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"accepted"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// First call: reports to server and succeeds
	reported := d.reportPresenceIfNeeded(ctx)
	if !reported {
		t.Fatal("expected first report to succeed, got false")
	}
	if got := callCount.Load(); got != 1 {
		t.Fatalf("call count = %d, want 1", got)
	}
	if err := receivedPayload.Validate(); err != nil {
		t.Fatalf("received invalid payload: %v", err)
	}
	if receivedPayload.ClientClass != "ordinary" {
		t.Errorf("clientClass = %q, want ordinary", receivedPayload.ClientClass)
	}

	// Verify local state was persisted
	today := identity.AlignedEpoch1d(time.Now().UTC())
	val, ok, err := d.DB.GetStat(ctx, statLastPresenceSuccessDay)
	if err != nil || !ok || val != today {
		t.Fatalf("statLastPresenceSuccessDay = %q (ok=%v err=%v), want %q", val, ok, err, today)
	}

	// Second call on the same day: suppressed locally without network request
	reported2 := d.reportPresenceIfNeeded(ctx)
	if reported2 {
		t.Fatal("expected second report on same day to be suppressed, got true")
	}
	if got := callCount.Load(); got != 1 {
		t.Fatalf("call count after suppressed call = %d, want 1", got)
	}
}

func TestPresenceRotationAcrossMidnight(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/presence" {
			callCount.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"accepted"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// Simulate yesterday was reported
	yesterday := "2026-09-11"
	if err := d.DB.SetStat(ctx, statLastPresenceSuccessDay, yesterday); err != nil {
		t.Fatal(err)
	}

	// Calling reportPresenceIfNeeded today should send because epoch changed
	reported := d.reportPresenceIfNeeded(ctx)
	if !reported {
		t.Fatal("expected report to succeed across day boundary, got false")
	}
	if got := callCount.Load(); got != 1 {
		t.Fatalf("call count = %d, want 1", got)
	}

	// Today should now be stamped
	today := identity.AlignedEpoch1d(time.Now().UTC())
	val, ok, _ := d.DB.GetStat(ctx, statLastPresenceSuccessDay)
	if !ok || val != today {
		t.Fatalf("stat = %q, want %q", val, today)
	}
}

func TestPresenceFailOpenDoesNotBlockUpload(t *testing.T) {
	// Server returns 500 Internal Server Error for presence
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/presence" {
			http.Error(w, "temporary server failure", http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/v1/evidence/batches" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":0,"rejected":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// uploadNow must fail-open on presence error: uploadNow must not return presence error
	n, err := d.uploadNow(ctx)
	if err != nil {
		t.Fatalf("uploadNow failed unexpectedly: %v", err)
	}
	if n != 0 {
		t.Fatalf("uploaded = %d, want 0", n)
	}

	// Presence failure must NOT stamp last-presence success, so it can retry later
	_, ok, _ := d.DB.GetStat(ctx, statLastPresenceSuccessDay)
	if ok {
		t.Fatal("statLastPresenceSuccessDay was stamped despite server error")
	}
}

func TestPresenceClientClassExplicit(t *testing.T) {
	tests := []struct {
		name        string
		cfgClass    string
		envClass    string
		wantPayload string
	}{
		{
			name:        "default ordinary",
			wantPayload: "ordinary",
		},
		{
			name:        "configured class in config",
			cfgClass:    "farm",
			wantPayload: "farm",
		},
		{
			name:        "env var override",
			envClass:    "verifier",
			wantPayload: "verifier",
		},
		{
			name:        "config class takes precedence over env var",
			cfgClass:    "operator",
			envClass:    "ci",
			wantPayload: "operator",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CSX_CLIENT_CLASS", tc.envClass)

			var receivedClass string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/presence" {
					var p domain.PresencePayload
					_ = json.NewDecoder(r.Body).Decode(&p)
					receivedClass = p.ClientClass
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"status":"accepted"}`))
					return
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()

			home := newTestHome(t, func(cfg *config.Config) {
				cfg.Mode = config.ModeCommunity
				cfg.ServerURL = srv.URL
				cfg.ClientClass = tc.cfgClass
			})
			d, err := New(home)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer d.Close()

			if !d.reportPresenceIfNeeded(context.Background()) {
				t.Fatal("reportPresenceIfNeeded failed")
			}
			if receivedClass != tc.wantPayload {
				t.Errorf("received class = %q, want %q", receivedClass, tc.wantPayload)
			}
		})
	}
}

func TestPresenceDisabledInLocalOnlyAndUninitialized(t *testing.T) {
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		t.Run("mode_"+mode, func(t *testing.T) {
			var called atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called.Store(true)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			home := newTestHome(t, func(cfg *config.Config) {
				cfg.Mode = mode
				cfg.ServerURL = srv.URL
			})
			d, err := New(home)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer d.Close()

			if d.reportPresenceIfNeeded(context.Background()) {
				t.Fatal("reportPresenceIfNeeded should return false in non-community mode")
			}
			if called.Load() {
				t.Fatalf("network request was made in mode %q", mode)
			}
		})
	}
}

func TestPresenceTriggeredByWarmNowAndSyncNow(t *testing.T) {
	var presenceCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/presence":
			presenceCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"accepted"}`))
		case "/v1/stats":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hotShards":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer d.Close()

	ctx := context.Background()

	// 1. warmNow triggers presence reporting
	_, _ = d.warmNow(ctx)
	if got := presenceCalls.Load(); got != 1 {
		t.Fatalf("presence calls after warmNow = %d, want 1", got)
	}

	// 2. Second warmNow on same day suppresses
	_, _ = d.warmNow(ctx)
	if got := presenceCalls.Load(); got != 1 {
		t.Fatalf("presence calls after second warmNow = %d, want 1", got)
	}

	// 3. Clear the stat to simulate next day, then verify SyncNow triggers presence reporting
	_ = d.DB.SetStat(ctx, statLastPresenceSuccessDay, "2026-09-11")
	_ = d.SyncNow(ctx)
	if got := presenceCalls.Load(); got != 2 {
		t.Fatalf("presence calls after SyncNow = %d, want 2", got)
	}
}
