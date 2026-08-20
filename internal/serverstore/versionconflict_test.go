package serverstore

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func conflictObs(t *testing.T, f *Fake, purl, project, stage, result string) {
	t.Helper()
	acc, rej, err := f.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-1", ProjectBucket: project,
		Package: purl, Stage: domain.Stage(stage), Result: domain.Result(result),
		ObservationCount: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}})
	if err != nil || acc != 1 {
		t.Fatalf("ingest %s: acc=%d rej=%+v err=%v", purl, acc, rej, err)
	}
}

// One project resolving the same package at two versions at once is the shape
// of dependency hell, and it is already in the data: 160 project-package
// pairs in production carry two versions or more. Nothing collects it, so
// nothing answers "why does this not work" with the one fact that usually
// explains it.
//
// A project bucket rotates monthly, so the same bucket IS the same project;
// two buckets are never merged.
func TestVersionConflictsFindTwoVersionsInOneProject(t *testing.T) {
	f := NewFake()
	conflictObs(t, f, "pkg:npm/ws@8.19.0", "proj-1", "PROJECT_TEST", "PASS")
	conflictObs(t, f, "pkg:npm/ws@7.5.0", "proj-1", "PROJECT_TEST", "FAIL")
	// A second project with one version is not a conflict.
	conflictObs(t, f, "pkg:npm/ws@8.19.0", "proj-2", "PROJECT_TEST", "PASS")

	rows, err := f.VersionConflicts(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("conflicts = %+v, want one pair", rows)
	}
	got := rows[0]
	if got.Lower != "7.5.0" || got.Higher != "8.19.0" {
		t.Errorf("pair = %s / %s, want 7.5.0 and 8.19.0", got.Lower, got.Higher)
	}
	if got.Projects != 1 {
		t.Errorf("projects = %d, want 1", got.Projects)
	}
	// The failure beside it is the whole point: a pair nobody ever saw break
	// is a coexistence, not a conflict.
	if got.Failing != 1 {
		t.Errorf("failing projects = %d, want 1", got.Failing)
	}
}

// A package every project pins to one version has nothing to report.
func TestVersionConflictsAreEmptyWhenEveryProjectAgrees(t *testing.T) {
	f := NewFake()
	conflictObs(t, f, "pkg:npm/ws@8.19.0", "proj-1", "PROJECT_TEST", "PASS")
	conflictObs(t, f, "pkg:npm/ws@8.19.0", "proj-2", "PROJECT_TEST", "FAIL")

	rows, err := f.VersionConflicts(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("conflicts = %+v, want none: every project used one version", rows)
	}
}
