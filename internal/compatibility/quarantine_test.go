package compatibility

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// seedSampleOnlyPackage stores one published sample for a package that has
// no observation evidence at all — the ordinary state of a seeded package,
// and the case where the sample is the shard's only reason to exist.
func seedSampleOnlyPackage(t *testing.T, store *serverstore.Fake, purl, sampleID string) {
	t.Helper()
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW", Goal: "check a caret requirement",
			Packages: []string{purl}, Contract: []string{"matches inside the range"},
		},
		Packages:        []string{purl},
		ContractCommand: []string{"cargo", "run", "--offline"},
		VerifierAdapter: "cargo@1",
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "cargo"},
	}
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "PUBLISHED", License: "MIT-0", SizeBytes: 1024, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

// `csx-server quarantine` tells the operator the sample "is hidden from
// search, shards and the explorer" and that "the next aggregation pass
// rebuilds the affected shards". For a seeded package whose only sample is
// the one being withdrawn, no pass ever did: shards are written for the
// keys a pass FINDS, the withdrawn sample is no longer among them, so the
// key was skipped and the old body — still naming the sample — stayed in
// the store and kept being served and synced.
//
// A takedown that does not take anything down is the worst kind of promise
// to get wrong: it is the one made to someone who has a legal right to have
// the content gone.
func TestQuarantineEmptiesTheShardItWasTheOnlySampleFor(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	const purl = "pkg:cargo/semver@1.0.28"
	sampleID := "sha256:" + strings.Repeat("cd", 32)
	seedSampleOnlyPackage(t, store, purl, sampleID)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	_, js, ok, err := store.GetShard(ctx, "cargo/semver/1")
	if err != nil || !ok || !strings.Contains(js, sampleID) {
		t.Fatalf("setup: the shard should carry the sample first (ok=%v err=%v)", ok, err)
	}
	beforeETag, _, _, _ := store.GetShard(ctx, "cargo/semver/1")

	if err := store.SetSampleQuarantine(ctx, sampleID, true, "takedown"); err != nil {
		t.Fatal(err)
	}

	// A fresh builder's first pass is a full one, which is what the command
	// tells the operator to wait for.
	b2 := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b2.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce after quarantine: %v", err)
	}

	etag, js, ok, err := store.GetShard(ctx, "cargo/semver/1")
	if err != nil {
		t.Fatal(err)
	}
	if ok && strings.Contains(js, sampleID) {
		t.Fatalf("the withdrawn sample is still served in the shard:\n%s", js)
	}
	// The ETag has to move too, or every client holding the old body keeps
	// getting 304 and never learns the sample is gone.
	if ok && etag == beforeETag {
		t.Error("shard body changed but the ETag did not: clients would never re-fetch it")
	}
}

// The retirement pass must not touch shards that are still fed — an empty
// rewrite there would delete live answers.
func TestRetirementLeavesLiveShardsAlone(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	keep := "sha256:" + strings.Repeat("ab", 32)
	drop := "sha256:" + strings.Repeat("cd", 32)
	seedSampleOnlyPackage(t, store, "pkg:cargo/semver@1.0.28", keep)
	seedSampleOnlyPackage(t, store, "pkg:cargo/serde@1.0.0", drop)

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSampleQuarantine(ctx, drop, true, "takedown"); err != nil {
		t.Fatal(err)
	}
	b2 := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b2.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if _, js, ok, _ := store.GetShard(ctx, "cargo/semver/1"); !ok || !strings.Contains(js, keep) {
		t.Errorf("the untouched package lost its sample from the shard:\n%s", js)
	}
	if _, js, ok, _ := store.GetShard(ctx, "cargo/serde/1"); ok && strings.Contains(js, drop) {
		t.Errorf("the quarantined sample survived:\n%s", js)
	}
}
