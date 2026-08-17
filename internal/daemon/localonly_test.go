package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// LOCAL ONLY promises that nothing about your projects leaves. A shard
// request names a package — GET /v1/shards/npm/left-pad/1, one per
// dependency, from one address — so warming from the local inventory sent
// the whole dependency tree to the server, which is exactly what the
// contract screen lists under what a COMMUNITY member contributes.
func TestLocalOnlyNeverWarmsFromYourOwnPackages(t *testing.T) {
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeLocalOnly
		c.PinnedPackages = []string{"pkg:npm/react@19.2.8"}
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()

	// A package the scan found in the user's own project.
	if err := d.DB.UpsertPackage(ctx,
		domain.PURL{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}, "PUBLIC"); err != nil {
		t.Fatal(err)
	}

	for _, k := range d.warmKeyList(ctx) {
		if strings.Contains(k, "left-pad") {
			t.Errorf("local-only warmed %q — the dependency inventory left the machine", k)
		}
	}

	// Community mode is what the inventory is for, and still uses it.
	home2 := newTestHome(t, func(c *config.Config) { c.Mode = config.ModeCommunity })
	d2, err := New(home2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d2.Close() })
	if err := d2.DB.UpsertPackage(ctx,
		domain.PURL{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0"}, "PUBLIC"); err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, k := range d2.warmKeyList(ctx) {
		if strings.Contains(k, "left-pad") {
			saw = true
		}
	}
	if !saw {
		t.Error("community mode stopped warming from the project inventory")
	}
}

func TestLocalOnlyAndUninitializedSyncNeverContactNetwork(t *testing.T) {
	for _, mode := range []string{config.ModeLocalOnly, config.ModeUninitialized} {
		t.Run(mode, func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			home := newTestHome(t, func(c *config.Config) {
				c.Mode = mode
				c.ServerURL = srv.URL
				c.PinnedPackages = []string{"pkg:npm/react@19.2.0"}
				c.IdleVerification = "unlimited"
				c.PeerListen = true
			})
			d, err := New(home)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()

			if d.Cross != nil {
				t.Fatal("non-community daemon wired a network cross-verifier")
			}
			if keys := d.warmKeyList(context.Background()); len(keys) != 0 {
				t.Fatalf("non-community warm list = %v, want empty", keys)
			}
			if got := d.SyncNow(context.Background()); got.WarmedKeys != 0 || got.UploadedBatches != 0 || got.UploadedReports != 0 {
				t.Fatalf("non-community sync did work: %+v", got)
			}

			// Exercise the automatic path too. Short cadences would make every
			// remote loop fire if startBackground accidentally launched one.
			d.uploadEvery = 10 * time.Millisecond
			d.warmEvery = 10 * time.Millisecond
			d.verifyEvery = 10 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			d.startBackground(ctx)
			time.Sleep(80 * time.Millisecond)
			cancel()

			if got := requests.Load(); got != 0 {
				t.Fatalf("mode %q made %d server requests", mode, got)
			}
		})
	}
}

func TestRunReloadsAConcurrentCommunityRevocation(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.ServerURL = srv.URL
	})
	d, err := New(home) // captures community before Run owns the lock
	if err != nil {
		t.Fatal(err)
	}
	d.uploadEvery = 10 * time.Millisecond
	d.warmEvery = 10 * time.Millisecond

	fresh, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Mode = config.ModeLocalOnly
	if err := fresh.Save(home); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("daemon did not become ready")
	}
	time.Sleep(80 * time.Millisecond)
	cancel()
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	if d.Cfg.Mode != config.ModeLocalOnly {
		t.Fatalf("daemon retained stale mode %q", d.Cfg.Mode)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("revoked daemon made %d server requests", got)
	}
}
