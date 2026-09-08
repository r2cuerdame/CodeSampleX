package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type errorInjectingStore struct {
	serverstore.Store
	getSampleErr error
}

func (e *errorInjectingStore) GetSample(ctx context.Context, sampleID string) (serverstore.SampleRow, bool, error) {
	if e.getSampleErr != nil {
		return serverstore.SampleRow{}, false, e.getSampleErr
	}
	return e.Store.GetSample(ctx, sampleID)
}

// TestWebStoreSampleMetaAndManifestDistinguishAbsenceFromError verifies that
// webStore.SampleMeta and webStore.SampleManifest:
// - Return (meta/manifest, true, nil) on healthy present samples.
// - Return (zero, false, nil) on truly absent samples (proven absence).
// - Return (zero, false, nil) on quarantined samples (quarantine hiding).
// - Return (zero, false, err) on infrastructure/store errors (distinct propagation).
func TestWebStoreSampleMetaAndManifestDistinguishAbsenceFromError(t *testing.T) {
	ctx := context.Background()
	fake := serverstore.NewFake()
	mock := &errorInjectingStore{Store: fake}
	w := &webStore{s: mock}

	manifest := `{"schemaVersion":1,"packages":["pkg:npm/axios@1.12.0"],"symbols":["axios.post"]}`

	// Save healthy sample
	if err := fake.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:valid",
		ManifestJSON: manifest,
		Status:       "CROSS_PASS",
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// Save quarantined sample
	if err := fake.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     "sha256:quarantined",
		ManifestJSON: manifest,
		Status:       "CROSS_PASS",
		Quarantined:  true,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Healthy sample
	meta, ok, err := w.SampleMeta(ctx, "sha256:valid")
	if err != nil || !ok || meta.SampleID != "sha256:valid" {
		t.Fatalf("healthy SampleMeta = (%+v, %v, %v), want (valid, true, nil)", meta, ok, err)
	}
	rawM, ok, err := w.SampleManifest(ctx, "sha256:valid")
	if err != nil || !ok || rawM != manifest {
		t.Fatalf("healthy SampleManifest = (%q, %v, %v), want (%q, true, nil)", rawM, ok, err, manifest)
	}

	// 2. Truly absent sample (proven absence: ok=false, err=nil)
	meta, ok, err = w.SampleMeta(ctx, "sha256:absent")
	if err != nil || ok {
		t.Fatalf("absent SampleMeta = (%+v, %v, %v), want (zero, false, nil)", meta, ok, err)
	}
	rawM, ok, err = w.SampleManifest(ctx, "sha256:absent")
	if err != nil || ok {
		t.Fatalf("absent SampleManifest = (%q, %v, %v), want (\"\", false, nil)", rawM, ok, err)
	}

	// 3. Quarantined sample (hidden from serving: ok=false, err=nil)
	meta, ok, err = w.SampleMeta(ctx, "sha256:quarantined")
	if err != nil || ok {
		t.Fatalf("quarantined SampleMeta = (%+v, %v, %v), want (zero, false, nil)", meta, ok, err)
	}
	rawM, ok, err = w.SampleManifest(ctx, "sha256:quarantined")
	if err != nil || ok {
		t.Fatalf("quarantined SampleManifest = (%q, %v, %v), want (\"\", false, nil)", rawM, ok, err)
	}

	// 4. Injected store error (e.g. pool timeout or query cancellation: ok=false, err!=nil)
	injectedErr := serverstore.ErrPoolBusy
	mock.getSampleErr = injectedErr

	meta, ok, err = w.SampleMeta(ctx, "sha256:valid")
	if !errors.Is(err, injectedErr) || ok {
		t.Fatalf("SampleMeta under error = (%+v, %v, %v), want (zero, false, %v)", meta, ok, err, injectedErr)
	}
	rawM, ok, err = w.SampleManifest(ctx, "sha256:valid")
	if !errors.Is(err, injectedErr) || ok {
		t.Fatalf("SampleManifest under error = (%q, %v, %v), want (\"\", false, %v)", rawM, ok, err, injectedErr)
	}

	// Also verify absent coordinate under error returns err (not false not-found)
	meta, ok, err = w.SampleMeta(ctx, "sha256:absent")
	if !errors.Is(err, injectedErr) || ok {
		t.Fatalf("SampleMeta on absent under error = (%+v, %v, %v), want (zero, false, %v)", meta, ok, err, injectedErr)
	}
	rawM, ok, err = w.SampleManifest(ctx, "sha256:absent")
	if !errors.Is(err, injectedErr) || ok {
		t.Fatalf("SampleManifest on absent under error = (%q, %v, %v), want (\"\", false, %v)", rawM, ok, err, injectedErr)
	}
}
