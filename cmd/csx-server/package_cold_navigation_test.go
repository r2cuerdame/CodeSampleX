package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// slowReadStore makes every read a cold package page performs cost a fixed
// delay, the way a builder pass on the two-core production host does. It is
// the whole reproduction of #426: the queries themselves are sub-millisecond
// idle (measured 2026-09-17 on production, GetSnapshotsForPURL 0.4 ms), so
// the seconds and the 503s come from how many gated round trips one page
// makes and from what the admission gate does when two pages make them at
// once.
type slowReadStore struct {
	serverstore.Store
	delay time.Duration
	reads atomic.Int64
	// How the cube got its snapshots: one gated read per release, or one
	// for the whole page.
	singleSnapshotReads atomic.Int64
	bulkSnapshotReads   atomic.Int64
}

func (s *slowReadStore) wait(ctx context.Context) error {
	s.reads.Add(1)
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *slowReadStore) ListPackageVersions(ctx context.Context, eco, name string) ([]serverstore.PackageRow, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.ListPackageVersions(ctx, eco, name)
}

func (s *slowReadStore) GetSnapshotsForPURL(ctx context.Context, purl string) ([]serverstore.SnapshotRow, error) {
	s.singleSnapshotReads.Add(1)
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetSnapshotsForPURL(ctx, purl)
}

func (s *slowReadStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.SnapshotKeys(ctx)
}

func (s *slowReadStore) VerifiedSamplesForPackages(ctx context.Context, names []string, limit int) ([]serverstore.SampleRow, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.VerifiedSamplesForPackages(ctx, names, limit)
}

func (s *slowReadStore) VerifiedSampleCodeCounts(ctx context.Context, prefix string) ([]serverstore.VerifiedSampleCodeCount, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.VerifiedSampleCodeCounts(ctx, prefix)
}

func (s *slowReadStore) WantedForPackage(ctx context.Context, eco, name string) ([]serverstore.WantedRow, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.WantedForPackage(ctx, eco, name)
}

func (s *slowReadStore) ListFailureClustersForPage(ctx context.Context, eco, name string, limit int) ([]serverstore.ClusterRow, int, error) {
	if err := s.wait(ctx); err != nil {
		return nil, 0, err
	}
	return s.Store.ListFailureClustersForPage(ctx, eco, name, limit)
}

func (s *slowReadStore) Dependencies(ctx context.Context, eco, name string) ([]serverstore.DependencyEdge, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.Dependencies(ctx, eco, name)
}

// seedColdPackages gives each package several released versions with a
// package-level snapshot and a few symbols, so a cold page assembles a cube
// the way a real npm package does.
func seedColdPackages(t *testing.T, store *serverstore.Fake, names []string, versions int) {
	t.Helper()
	now := time.Now()
	for _, name := range names {
		for v := 1; v <= versions; v++ {
			purl := fmt.Sprintf("pkg:npm/%s@%d.0.0", name, v)
			row := serverstore.PackageRow{
				PURL: purl, Ecosystem: "npm", Name: name, Version: fmt.Sprintf("%d.0.0", v),
				Major: fmt.Sprint(v), Publicness: "PUBLIC", FirstSeen: now, LastSeen: now,
			}
			if err := store.UpsertPackage(t.Context(), row); err != nil {
				t.Fatal(err)
			}
			doc := `{"rows":[{"os":"linux","runtime":"node 22","by_stage":{"VERIFIED":3}}]}`
			if err := store.PutSnapshot(t.Context(), purl, "", doc); err != nil {
				t.Fatal(err)
			}
			for _, sym := range []string{"readFile", "writeFile"} {
				if err := store.PutSnapshot(t.Context(), purl, sym, doc); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func TestConcurrentColdPackageNavigationNever503sWhenPoolIsIdle(t *testing.T) {
	underlying := serverstore.NewFake()
	names := []string{"fs-extra", "tmp", "got", "globals", "jsonfile", "strip-ansi"}
	seedColdPackages(t, underlying, names, 3)
	// Longer than packageLoadAdmissionWait: on production during a builder
	// pass a sub-millisecond read costs hundreds of milliseconds of CPU wait.
	store := &slowReadStore{Store: underlying, delay: packageLoadAdmissionWait + 100*time.Millisecond}
	mux := BuildMux(serverstore.ServerConfig{PublicCheck: "trust", BlobDir: t.TempDir()}, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The crawl in #426: several never-visited package pages navigated at
	// once. Nothing else is using the database.
	type result struct {
		name    string
		status  int
		elapsed time.Duration
	}
	results := make([]result, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			resp, err := http.Get(srv.URL + "/npm/" + name)
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
			results[i] = result{name: name, status: resp.StatusCode, elapsed: time.Since(started)}
		}()
	}
	wg.Wait()
	for _, r := range results {
		if r.status != http.StatusOK {
			t.Errorf("/npm/%s = %d after %v, want 200: an idle pool must never turn a cache miss into a 503", r.name, r.status, r.elapsed)
		}
	}
	t.Logf("cold reads issued for %d pages: %d", len(names), store.reads.Load())
}

func (s *slowReadStore) GetSnapshotsForPURLs(ctx context.Context, purls []string) ([]serverstore.SnapshotRow, error) {
	s.bulkSnapshotReads.Add(1)
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return s.Store.GetSnapshotsForPURLs(ctx, purls)
}

// A cold package page used to read one release's snapshots per admission
// slot, in sequence: six releases were six gated round trips before the cube
// could render, and every dependency row was one more. One page, one read.
func TestColdPackagePageReadsAllReleaseSnapshotsInOneRoundTrip(t *testing.T) {
	underlying := serverstore.NewFake()
	seedColdPackages(t, underlying, []string{"fs-extra"}, 4)
	store := &slowReadStore{Store: underlying}
	mux := BuildMux(serverstore.ServerConfig{PublicCheck: "trust", BlobDir: t.TempDir()}, store)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/npm/fs-extra")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cold package page = %d, want 200", resp.StatusCode)
	}
	if got := store.singleSnapshotReads.Load(); got != 0 {
		t.Errorf("cold page issued %d per-release snapshot reads, want 0: the cube must read its releases together", got)
	}
	if got := store.bulkSnapshotReads.Load(); got != 1 {
		t.Errorf("cold page issued %d bulk snapshot reads, want exactly 1", got)
	}

	// Warm: the bulk read populated every per-release entry, so the second
	// visit reads nothing.
	before := store.reads.Load()
	resp, err = http.Get(srv.URL + "/npm/fs-extra")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := store.reads.Load() - before; got != 0 {
		t.Errorf("warm package page issued %d store reads, want 0", got)
	}
}
