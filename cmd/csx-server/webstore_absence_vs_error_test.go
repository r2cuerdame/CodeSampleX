package main

// The adapter's caches are the last place a transient failure can be turned
// into a remembered absence (#445). A failed read must reach the page as an
// error, never as an empty list, and must not leave a negative entry behind
// that keeps answering "absent" after the store recovers.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

// flakyReadStore fails a whole read family for as long as `failing` is set
// and answers from the Fake otherwise.
type flakyReadStore struct {
	*serverstore.Fake
	failing atomic.Bool
	err     error
}

func (s *flakyReadStore) fail() error {
	if s.failing.Load() {
		return s.err
	}
	return nil
}

func (s *flakyReadStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	return s.Fake.SnapshotKeys(ctx)
}

func (s *flakyReadStore) GetSnapshotsForPURL(ctx context.Context, purl string) ([]serverstore.SnapshotRow, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	return s.Fake.GetSnapshotsForPURL(ctx, purl)
}

func (s *flakyReadStore) GetSnapshotsForPURLs(ctx context.Context, purls []string) ([]serverstore.SnapshotRow, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	return s.Fake.GetSnapshotsForPURLs(ctx, purls)
}

func (s *flakyReadStore) ListPackageVersions(ctx context.Context, eco, name string) ([]serverstore.PackageRow, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	return s.Fake.ListPackageVersions(ctx, eco, name)
}

func (s *flakyReadStore) GetSample(ctx context.Context, id string) (serverstore.SampleRow, bool, error) {
	if err := s.fail(); err != nil {
		return serverstore.SampleRow{}, false, err
	}
	return s.Fake.GetSample(ctx, id)
}

func (s *flakyReadStore) VerifiedSamplesForPackages(ctx context.Context, names []string, limit int) ([]serverstore.SampleRow, error) {
	if err := s.fail(); err != nil {
		return nil, err
	}
	return s.Fake.VerifiedSamplesForPackages(ctx, names, limit)
}

func newFlakyWebStore(t *testing.T) (*webStore, *flakyReadStore) {
	t.Helper()
	ctx := context.Background()
	fake := serverstore.NewFake()
	const purl = "pkg:npm/axios@1.12.0"
	if err := fake.UpsertPackage(ctx, serverstore.PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "axios", Version: "1.12.0", LastSeen: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"", "axios.post"} {
		if err := fake.PutSnapshot(ctx, purl, sym, `{"schemaVersion":1,"purl":"`+purl+`","symbol":"`+sym+`","rows":[]}`); err != nil {
			t.Fatal(err)
		}
	}
	if err := fake.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:present",
		ManifestJSON: `{"schemaVersion":1,"packages":["` + purl + `"],"symbols":["axios.post"]}`,
		Status:       "CROSS_PASS",
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := fake.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: "receipt-present", SampleID: "sha256:present",
		ContractResult: "PASS", ReceiptJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	flaky := &flakyReadStore{Fake: fake, err: serverstore.ErrPoolBusy}
	return &webStore{s: flaky}, flaky
}

func interactiveCtx() context.Context {
	return serverstore.WithQueryClass(context.Background(), serverstore.ClassInteractive)
}

// PackageSymbols used to answer a failed target-index read with (nil, nil)
// (#396), which the version page could only read as "no symbols" -- and, with
// nothing else on the page, as a 404.
func TestPackageSymbolsPropagatesIndexFailureInsteadOfEmpty(t *testing.T) {
	w, flaky := newFlakyWebStore(t)
	ctx := interactiveCtx()
	flaky.failing.Store(true)

	syms, err := w.PackageSymbols(ctx, "npm", "axios", "1.12.0")
	if err == nil {
		t.Fatalf("PackageSymbols under pool pressure = (%v, nil); want an error, not an empty list", syms)
	}
	if !errors.Is(err, serverstore.ErrPoolBusy) {
		t.Fatalf("PackageSymbols error = %v, want ErrPoolBusy", err)
	}
	spread, err := w.SymbolPackageSpread(ctx, "npm", []string{"axios.post"})
	if err == nil {
		t.Fatalf("SymbolPackageSpread under pool pressure = (%v, nil); want an error", spread)
	}

	// The failure arms a short deferral (snapshotLoadRetryDefer) so a
	// stampede does not repeat it. While deferring the answer is STILL an
	// error -- never an empty list with nil.
	flaky.failing.Store(false)
	syms, err = w.PackageSymbols(ctx, "npm", "axios", "1.12.0")
	if err == nil {
		t.Fatalf("deferring PackageSymbols = (%v, nil); want an error while the deferral holds", syms)
	}

	// Once the deferral passes, the failure has left no negative entry
	// behind: the next read goes to the store and finds the symbols.
	w.targetsMu.Lock()
	w.targetsRetryAt = time.Time{}
	w.targetsMu.Unlock()
	syms, err = w.PackageSymbols(ctx, "npm", "axios", "1.12.0")
	if err != nil || len(syms) != 1 || syms[0] != "axios.post" {
		t.Fatalf("recovered PackageSymbols = (%v, %v), want ([axios.post], nil)", syms, err)
	}
}

// A failed sample read must not be remembered by the coalescing group as
// "absent": the next reader after the store recovers sees the row.
func TestSampleMetaFailureIsNotCachedAsAbsence(t *testing.T) {
	w, flaky := newFlakyWebStore(t)
	ctx := interactiveCtx()

	flaky.failing.Store(true)
	meta, ok, err := w.SampleMeta(ctx, "sha256:present")
	if err == nil || ok {
		t.Fatalf("SampleMeta under pressure = (%+v, %v, %v), want (zero, false, err)", meta, ok, err)
	}
	if _, ok, err := w.SampleManifest(ctx, "sha256:present"); err == nil || ok {
		t.Fatalf("SampleManifest under pressure = (ok=%v, err=%v), want (false, err)", ok, err)
	}

	flaky.failing.Store(false)
	meta, ok, err = w.SampleMeta(ctx, "sha256:present")
	if err != nil || !ok || meta.SampleID != "sha256:present" {
		t.Fatalf("recovered SampleMeta = (%+v, %v, %v), want the row", meta, ok, err)
	}
}

// A failed per-release snapshot load arms a deferral so a stampede of
// visitors does not repeat the failed read. During that deferral the answer
// is still an ERROR (pool busy), never (absent, nil): the release may well
// exist.
func TestSnapshotLoadFailureAnswersErrorNotAbsenceWhileDeferring(t *testing.T) {
	w, flaky := newFlakyWebStore(t)
	ctx := interactiveCtx()
	const purl = "pkg:npm/axios@1.12.0"

	flaky.failing.Store(true)
	flaky.err = errors.New("read tcp: connection reset by peer")
	raw, ok, err := w.SnapshotJSONWithError(ctx, purl, "")
	if err == nil {
		t.Fatalf("first snapshot read under failure = (%q, %v, nil); want an error", raw, ok)
	}

	// The store is healthy again but the lane is deferring: still an error.
	flaky.failing.Store(false)
	raw, ok, err = w.SnapshotJSONWithError(ctx, purl, "")
	if err == nil && !ok {
		t.Fatalf("deferring snapshot read = (%q, false, nil): a deferral rendered as absence", raw)
	}
	if err != nil && !serverstore.IsPoolBusy(err) {
		t.Fatalf("deferring snapshot read error = %v, want a pool-busy classification", err)
	}

	// PrefetchSnapshots leaves a deferring lane alone and never records the
	// release as loaded, so the bulk path cannot manufacture absence either.
	if err := w.PrefetchSnapshots(ctx, []string{purl}); err != nil {
		t.Fatalf("PrefetchSnapshots during deferral = %v, want nil (skipped)", err)
	}
	if _, loaded := w.purlsLoaded.Load(purl); loaded {
		t.Fatalf("PrefetchSnapshots marked a deferring release as loaded")
	}
}

// PackageVersions is the read that decides whether a package exists at all.
// A failure must reach the page, and the package must still exist afterward.
func TestPackageVersionsFailureIsNotCachedAsAbsence(t *testing.T) {
	w, flaky := newFlakyWebStore(t)
	ctx := interactiveCtx()

	flaky.failing.Store(true)
	versions, err := w.PackageVersions(ctx, "npm", "axios")
	if err == nil {
		t.Fatalf("PackageVersions under pressure = (%v, nil), want an error", versions)
	}
	flaky.failing.Store(false)
	versions, err = w.PackageVersions(ctx, "npm", "axios")
	if err != nil || len(versions) != 1 || versions[0] != "1.12.0" {
		t.Fatalf("recovered PackageVersions = (%v, %v), want ([1.12.0], nil)", versions, err)
	}
}

// PackageSamples caches only successful reads; a failed one is an error to
// the caller and nothing to the cache.
func TestPackageSamplesFailureIsNotCachedAsEmpty(t *testing.T) {
	w, flaky := newFlakyWebStore(t)
	ctx := interactiveCtx()

	flaky.failing.Store(true)
	items, err := w.PackageSamples(ctx, "npm", "axios", 10)
	if err == nil {
		t.Fatalf("PackageSamples under pressure = (%v, nil), want an error", items)
	}
	flaky.failing.Store(false)
	items, err = w.PackageSamples(ctx, "npm", "axios", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("recovered PackageSamples = (%d items, %v), want 1 item", len(items), err)
	}
	var _ web.Store = w
}
