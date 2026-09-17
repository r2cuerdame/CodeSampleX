package serverstore

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Both stores read the same CLI rows from the same ingested batches: the
// planner is one Go pass, so the only way the two can drift is here.
func TestIntegrationCLIObservationsParity(t *testing.T) {
	pg := openTestPG(t)
	fake := NewFake()
	ctx := context.Background()
	code := 0
	batches := []domain.ObservationBatch{{
		SchemaVersion: 2, Epoch: "2026-09-18", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.47.2", Symbol: "farm:status --short",
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "linux", Arch: "amd64"},
		Stage:         domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 2,
		TerminationKind: domain.TerminationExit, ExitCode: &code, ActualToolchain: "farm",
	}, {
		SchemaVersion: 2, Epoch: "2026-09-18", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/git@2.51.0", Symbol: "field:worktree add <path>",
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: "Windows", Arch: "amd64"},
		Stage:         domain.StageProjectProcess, Result: domain.ResultFail, ObservationCount: 1,
		ErrorFingerprint: "sha256:" + strings.Repeat("ab", 32), ErrorCode: "EXIT_128",
	}, {
		SchemaVersion: 2, Epoch: "2026-09-18", AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:npm/axios@1.12.0",
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.18"},
		Stage:         domain.StageUsed, Result: domain.ResultPass, ObservationCount: 1,
	}}
	for _, store := range []Store{pg, fake} {
		accepted, rejected, err := store.IngestBatches(ctx, batches)
		if err != nil || accepted != len(batches) {
			t.Fatalf("%T ingest: accepted=%d rejected=%+v err=%v", store, accepted, rejected, err)
		}
	}
	got, err := pg.ListCLIObservations(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := fake.ListCLIObservations(ctx, 0)
	if len(got) != 2 || len(want) != 2 {
		t.Fatalf("pg=%+v fake=%+v", got, want)
	}
	for i := range got {
		if got[i].PURL != want[i].PURL || got[i].Symbol != want[i].Symbol || got[i].OS != want[i].OS ||
			got[i].Result != want[i].Result || got[i].Count != want[i].Count || got[i].LastSeen.IsZero() {
			t.Fatalf("row %d: pg=%+v fake=%+v", i, got[i], want[i])
		}
	}
	if got[1].OS != "windows" {
		t.Fatalf("OS is not lower-cased: %+v", got[1])
	}
}
