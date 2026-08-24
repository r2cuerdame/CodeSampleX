package compatibility

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A failure cluster is a per-PACKAGE aggregate, and UpsertFailureCluster
// replaces observation_count, versions and env_summary outright. On an
// incremental pass the builder held only the versions that had changed, so
// a change to one version rewrote the whole cluster from that slice: the
// count collapsed, the other version vanished from the list, and the
// environment summary flipped to whichever machine happened to report last.
//
// That cluster is what the search shows a caller as a known failure of the
// package they asked about.
func TestAnIncrementalPassDoesNotShrinkAFailureCluster(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)

	old := "pkg:npm/axios@0.27.2"
	recent := "pkg:npm/axios@1.12.0"
	exitCode := 1
	fp := domain.FailureFingerprint(domain.StageProjectTest, domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}, "E_BOOM", "E_BOOM normalized failure")

	// 100 failures on the old version from windows, 50 on the new from linux.
	ingest(t, store, old, "get", fp, "windows", 100)
	ingest(t, store, recent, "get", fp, "linux", 50)

	b := &Builder{Store: store, Now: func() time.Time { return now }}
	if err := b.RunOnce(ctx); err != nil { // full pass
		t.Fatal(err)
	}
	before := clusterFor(t, store, "npm", "axios", fp)
	if before.ObservationCount != 150 {
		t.Fatalf("full pass count = %d, want 150", before.ObservationCount)
	}

	// Only the new version changes; the pass goes incremental.
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{Targets: []serverstore.SnapshotTarget{{PURL: recent, Symbol: "get"}}}, nil
	}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	after := clusterFor(t, store, "npm", "axios", fp)
	if after.ObservationCount != 150 {
		t.Errorf("after an incremental pass count = %d, want 150 — the cluster was rebuilt from one version",
			after.ObservationCount)
	}
	if !strings.Contains(after.VersionsJSON, "0.27.2") {
		t.Errorf("the older version vanished from the cluster: %s", after.VersionsJSON)
	}
}

func ingest(t *testing.T, store *serverstore.Fake, purl, symbol, fp, os string, count int) {
	t.Helper()
	exitCode := 1
	_, rejected, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-15", AnonID: "anon" + os, ProjectBucket: "proj" + os,
		Package: purl, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: os, Arch: "x64"},
		Stage:       domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFingerprint: fp, ErrorCode: "E_BOOM", ObservationCount: count,
		TerminationKind: domain.TerminationExit, ExitCode: &exitCode,
		ErrorSummary: "E_BOOM normalized failure", EvidenceQuality: domain.EvidenceComplete,
	}})
	if err != nil || len(rejected) > 0 {
		t.Fatalf("ingest: %v %v", err, rejected)
	}
}

func clusterFor(t *testing.T, store *serverstore.Fake, eco, name, fp string) serverstore.ClusterRow {
	t.Helper()
	rows, err := store.ListFailureClusters(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rows {
		if c.Ecosystem == eco && c.ErrorFingerprint == fp {
			return c
		}
	}
	t.Fatalf("no cluster for %s/%s %s in %d rows", eco, name, fp, len(rows))
	return serverstore.ClusterRow{}
}

var _ = json.Marshal
