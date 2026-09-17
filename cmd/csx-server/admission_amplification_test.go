package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Issue #433 / v0.1.195 regression, end to end through the production adapter.
//
// The package cache-miss admission gate is the site's load shedder: when its
// slots are full, a request stands at it for at most its admission allowance
// (packageLoadAdmissionBudget -- one per request across every cold read it
// makes, #426) and is then refused with ErrPoolBusy, having touched no
// connection. v0.1.195 classified that refusal as a transient read error and
// retried it, so every refused read paid the wait three times instead of
// once -- while holding a request goroutine, and while the retries
// themselves kept the gate full.
//
// This test pins the shed: once the gate is saturated, a further cold page
// gives up after roughly one allowance -- not before it, which was the
// idle-pool 503 of #426, and not three times it -- and the routes that must
// stay up stay up.

// admissionBlockStore holds every "slow-*" package load open until released,
// which is how the admission gate is driven to saturation from outside.
type admissionBlockStore struct {
	serverstore.Store
	entered chan struct{}
	unblock chan struct{}
}

func (s *admissionBlockStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	if !strings.HasPrefix(name, "slow-") {
		return s.Store.ListPackageVersions(ctx, ecosystem, name)
	}
	select {
	case s.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-s.unblock:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Store.ListPackageVersions(ctx, ecosystem, name)
}

func TestSaturatedAdmissionGateShedsInsteadOfRetrying(t *testing.T) {
	underlying := serverstore.NewFake()
	now := time.Now()
	axios := serverstore.PackageRow{
		PURL: "pkg:npm/axios@1.0.0", Ecosystem: "npm", Name: "axios", Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC", FirstSeen: now, LastSeen: now,
	}
	if err := underlying.UpsertPackage(t.Context(), axios); err != nil {
		t.Fatal(err)
	}
	if err := underlying.PutSnapshot(t.Context(), axios.PURL, "", `{"rows":[]}`); err != nil {
		t.Fatal(err)
	}

	store := &admissionBlockStore{
		Store:   underlying,
		entered: make(chan struct{}, packageLoadSlotCount*2),
		unblock: make(chan struct{}),
	}
	srv := httptest.NewServer(BuildMux(serverstore.ServerConfig{
		PublicCheck: "trust",
		BlobDir:     t.TempDir(),
	}, store))
	defer srv.Close()

	// Warm one package so the "stays responsive" assertions below are about
	// the gate rather than about a cold read of their own.
	if resp, err := http.Get(srv.URL + "/npm/axios"); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("priming /npm/axios = %d, want 200", resp.StatusCode)
		}
	}

	// Occupy every admission slot with a load that will not return.
	for i := range packageLoadSlotCount {
		go func() {
			resp, _ := http.Get(fmt.Sprintf("%s/npm/slow-%d", srv.URL, i))
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}
	for range packageLoadSlotCount {
		select {
		case <-store.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out saturating the package admission gate")
		}
	}
	defer close(store.unblock)

	// A further cold page can only be refused. The question this test asks is
	// how long it takes to say so.
	started := time.Now()
	resp, err := http.Get(srv.URL + "/npm/some-other-cold-package")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	elapsed := time.Since(started)
	t.Logf("refused cold page: status=%d elapsed=%v (admission allowance %v, slots %d)",
		resp.StatusCode, elapsed, packageLoadAdmissionBudget, packageLoadSlotCount)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a saturated admission gate answered %d, want 503 (never 404: that is proven absence)", resp.StatusCode)
	}
	// Not before the allowance: a refusal with time still on the clock is the
	// gate deciding for the pool, which is how an idle pool served 503s (#426).
	if elapsed < packageLoadAdmissionBudget {
		t.Errorf("refused cold page took %v, want at least its %v allowance before being shed",
			elapsed, packageLoadAdmissionBudget)
	}
	// One allowance, plus room for scheduling. Retrying the refusal costs
	// (1+maxReadRetries) allowances, which is what this bound excludes.
	if limit := packageLoadAdmissionBudget + 2*packageLoadAdmissionWait; elapsed >= limit {
		t.Errorf("refused cold page took %v, want under %v: the admission refusal is being retried "+
			"instead of shed, which is what turned a busy minute into 13-25s 503s (#433)",
			elapsed, limit)
	}

	// The routes that must survive saturation.
	for _, path := range []string{"/healthz", "/npm/axios"} {
		started := time.Now()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d while the package gate was saturated, want 200", path, resp.StatusCode)
		}
		if elapsed := time.Since(started); elapsed >= packageLoadAdmissionWait {
			t.Errorf("%s took %v while the package gate was saturated; it must not queue behind it",
				path, elapsed)
		}
	}
}
