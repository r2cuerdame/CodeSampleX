package compatibility

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The production reproduction for the 2026-08-26 deploy that rolled back.
//
// One symbol reaches the server under two spellings: anonymous evidence
// carries the scanner's qualified name, a signed receipt carries the author's
// bare one. symbolSpellings exists so a read for either finds both — which is
// right for a per-target read, and wrong the moment those per-target reads are
// summed into one per-package bucket. The qualified rows are then returned
// once for the qualified target and once for the bare one, and the failure
// cluster counts the same observations twice.
//
// It is not a stable doubling either. A pass only reads the targets it is
// rebuilding, so the ledger lands on N or 2N depending on which targets that
// pass happened to cover — and a server restart forces a full pass, which is
// why every deploy re-materializes it. The production deploy transaction
// refuses to commit when that ledger drops, so an aggregation artefact
// became a rollback with no evidence lost.
func TestOneSymbolFiledUnderTwoSpellingsIsCountedOnce(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	const (
		purl      = "pkg:golang/github.com/google/uuid@v1.6.0"
		pkgName   = "github.com/google/uuid"
		bare      = "Parse"
		qualified = pkgName + "." + bare
		failures  = 260
	)

	// Anonymous evidence: the scanner's qualified spelling.
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "anon-spelling", ProjectBucket: "proj-spelling",
		Package: purl, Symbol: qualified, SymbolConfidence: domain.SymbolProbable,
		Environment: envGo(),
		Stage:       domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFingerprint: "sha256:" + strings.Repeat("2", 64), ObservationCount: failures,
	}}); err != nil || len(rejected) > 0 {
		t.Fatalf("ingest: %v %v", err, rejected)
	}

	// A signed receipt for the same package: the author's bare spelling.
	// This is what puts the second snapshot target on the same symbol.
	caseID := "case:uuid-parse"
	manifest := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, CaseID: caseID, Kind: "HOW", Goal: "parse a UUID",
			Packages: []string{purl}, Contract: []string{"parses a UUID"},
		},
		Packages: []string{purl}, Symbols: []string{bare},
		Environment: envGo(), License: "MIT-0",
		ContractCommand: []string{"go", "test", "./..."}, VerifierAdapter: "go@1",
	}
	sampleID := "sha256:" + strings.Repeat("c", 64)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, CaseID: caseID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "CROSS_PASS", License: "MIT-0", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
		EnvironmentHash: manifest.Environment.Normalize().Hash(), Environment: manifest.Environment,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: []string{purl}, VerifierAdapter: manifest.VerifierAdapter,
		SandboxCapability: domain.CapContainerRun, CreatedAt: testNow.Format(time.RFC3339),
		PeerID: "ed25519:cccccccccccccccc",
	}
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: "PASS", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	// Both spellings must actually be live targets, or this test proves
	// nothing about the bucket they are summed into.
	targets, err := store.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var sawBare, sawQualified bool
	for _, target := range targets {
		if target.PURL != purl {
			continue
		}
		sawBare = sawBare || target.Symbol == bare
		sawQualified = sawQualified || target.Symbol == qualified
	}
	if !sawBare || !sawQualified {
		t.Fatalf("targets = %+v, want both %q and %q", targets, bare, qualified)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListFailureClusters(ctx, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	var ledger int64
	for _, row := range rows {
		ledger += row.ObservationCount
	}
	if ledger != failures {
		t.Fatalf("cluster-observation ledger = %d over %d rows, want %d — the same evidence is counted once per symbol spelling",
			ledger, len(rows), failures)
	}
}

// The same evidence must produce the same ledger whether the pass rebuilt
// every target or only the one that changed. Otherwise the number a deploy
// compares before and after moves for no reason but the pass shape.
func TestClusterLedgerDoesNotDependOnWhichTargetsThePassRebuilt(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return testNow }

	const (
		purl      = "pkg:golang/github.com/google/uuid@v1.6.0"
		pkgName   = "github.com/google/uuid"
		qualified = pkgName + ".Parse"
		failures  = 12
	)
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "anon-shape", ProjectBucket: "proj-shape",
		Package: purl, Symbol: qualified, SymbolConfidence: domain.SymbolProbable,
		Environment: envGo(),
		Stage:       domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFingerprint: "sha256:" + strings.Repeat("3", 64), ObservationCount: failures,
	}}); err != nil || len(rejected) > 0 {
		t.Fatalf("ingest: %v %v", err, rejected)
	}

	b := &Builder{Store: store, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	full := clusterLedger(t, store, pkgName)
	if want := failEvidence(t, store, purl, qualified); full != want {
		t.Fatalf("full pass ledger = %d, want %d", full, want)
	}

	// A second pass is incremental: it reads only what changed since the
	// last one, and rebuilds the package's clusters from that plus whatever
	// the remaining live targets still hold. The ledger must describe the
	// evidence, not the pass.
	if _, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "anon-shape-2", ProjectBucket: "proj-shape-2",
		Package: purl, Symbol: qualified, SymbolConfidence: domain.SymbolProbable,
		Environment: envGo(),
		Stage:       domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFingerprint: "sha256:" + strings.Repeat("3", 64), ObservationCount: failures + 1,
	}}); err != nil || len(rejected) > 0 {
		t.Fatalf("ingest: %v %v", err, rejected)
	}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	incremental := clusterLedger(t, store, pkgName)

	if incremental < full {
		t.Fatalf("cluster-observation ledger fell from %d to %d while evidence only grew", full, incremental)
	}
	if want := failEvidence(t, store, purl, qualified); incremental != want {
		t.Fatalf("incremental pass ledger = %d, want %d", incremental, want)
	}
}

// failEvidence is the FAIL observation total the clusters are derived from.
func failEvidence(t *testing.T, store *serverstore.Fake, purl, symbol string) int64 {
	t.Helper()
	rows, err := store.EvidenceForTarget(context.Background(), purl, symbol)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range rows {
		if row.Result == string(domain.ResultFail) {
			total += row.ObservationCount
		}
	}
	return total
}

func clusterLedger(t *testing.T, store *serverstore.Fake, pkgName string) int64 {
	t.Helper()
	rows, err := store.ListFailureClusters(context.Background(), pkgName)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, row := range rows {
		total += row.ObservationCount
	}
	return total
}

func envGo() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "golang", OS: "linux", Arch: "amd64",
		Runtime: "go", RuntimeVersion: "1.26",
	}
}
