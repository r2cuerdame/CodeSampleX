package serverstore

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestFakeReceiptResolvedPackagesCreateExactTargetsAndDirtyKeys(t *testing.T) {
	ctx := context.Background()
	store := NewFake()
	declared := "pkg:npm/axios@1.0.0"
	resolved := "pkg:npm/axios@2.1.3"
	sampleID := "sha256:" + strings.Repeat("c", 64)
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "post JSON",
			Packages: []string{declared}, Contract: []string{"posts JSON"}},
		Packages: []string{declared}, Symbols: []string{"axios.post"},
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64"},
		License:     "MIT-0", ContractCommand: []string{"node", "test.mjs"}, VerifierAdapter: "node-typescript@1",
	}
	if err := store.SaveSample(ctx, SampleRow{
		SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
	}); err != nil {
		t.Fatal(err)
	}

	receipt := domain.VerificationReceipt{
		SchemaVersion: 1, SampleID: sampleID, CaseID: "case:stable",
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: []string{resolved},
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "sha256:v1", SampleID: sampleID,
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: "PASS",
	}); err != nil {
		t.Fatal(err)
	}
	if targets, err := store.ListSnapshotTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("v1 receipt targets = %v err=%v", targets, err)
	}

	receipt.SchemaVersion = 2
	if err := store.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "sha256:v2", SampleID: sampleID,
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: "PASS",
	}); err != nil {
		t.Fatal(err)
	}
	targets, err := store.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := map[SnapshotTarget]bool{
		{PURL: resolved, Symbol: ""}:           true,
		{PURL: resolved, Symbol: "axios.post"}: true,
	}
	if len(targets) != len(wantTargets) {
		t.Fatalf("targets = %+v", targets)
	}
	for _, target := range targets {
		if !wantTargets[target] {
			t.Errorf("unexpected target %+v", target)
		}
	}

	changes, err := store.ChangedSince(ctx, store.now())
	if err != nil {
		t.Fatal(err)
	}
	wantDirty := map[string]bool{declared: true, resolved: true}
	for _, purl := range changes.SamplePURLs {
		delete(wantDirty, purl)
	}
	if len(wantDirty) != 0 {
		t.Fatalf("missing dirty purls: %v (got %v)", wantDirty, changes.SamplePURLs)
	}

	if err := store.SetSampleQuarantine(ctx, sampleID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if targets, err := store.ListSnapshotTargets(ctx); err != nil || len(targets) != 0 {
		t.Fatalf("quarantined receipt targets = %v err=%v", targets, err)
	}
	changes, err = store.ChangedSince(ctx, store.now())
	if err != nil {
		t.Fatal(err)
	}
	wantDirty = map[string]bool{declared: true, resolved: true}
	for _, purl := range changes.SamplePURLs {
		delete(wantDirty, purl)
	}
	if len(wantDirty) != 0 {
		t.Fatalf("quarantine did not dirty historical resolved shards: %v", wantDirty)
	}
}

func TestResolvedPackageStringsFailsClosed(t *testing.T) {
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, Stages: map[string]string{"resolve": "PASS"},
		ResolvedPackages: []string{"pkg:npm/axios@1.12.4", "pkg:npm/axios@^1"},
	}
	if got := resolvedPackageStrings(string(domain.MustCanonicalJSON(receipt))); len(got) != 0 {
		t.Fatalf("partially accepted malformed receipt packages: %v", got)
	}
}

func TestFakeSnapshotKeysAndDeleteAreScoped(t *testing.T) {
	ctx := context.Background()
	store := NewFake()
	first := SnapshotTarget{PURL: "pkg:npm/a@1.0.0", Symbol: "a.one"}
	second := SnapshotTarget{PURL: "pkg:npm/a@1.0.0", Symbol: "a.two"}
	for _, target := range []SnapshotTarget{second, first} {
		if err := store.PutSnapshot(ctx, target.PURL, target.Symbol, `{}`); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := store.SnapshotKeys(ctx)
	if err != nil || len(keys) != 2 || keys[0] != first || keys[1] != second {
		t.Fatalf("snapshot keys = %+v err=%v", keys, err)
	}
	if err := store.DeleteSnapshots(ctx, []SnapshotTarget{first}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.GetSnapshot(ctx, first.PURL, first.Symbol); ok {
		t.Fatal("deleted snapshot survived")
	}
	if _, ok, _ := store.GetSnapshot(ctx, second.PURL, second.Symbol); !ok {
		t.Fatal("deleting one snapshot removed another")
	}
}
