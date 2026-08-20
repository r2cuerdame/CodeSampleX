package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func conflictObs(t *testing.T, f *Fake, purl, project, stage, result string) {
	t.Helper()
	conflictObsOn(t, f, purl, project, "2026-08-20", stage, result, "ERR_X")
}

func conflictObsOn(t *testing.T, f *Fake, purl, project, epoch, stage, result, code string) {
	t.Helper()
	fp := ""
	if result == string(domain.ResultFail) {
		fp = "sha256:" + strings_Repeat("ab", 32)
	} else {
		code = ""
	}
	acc, rej, err := f.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: epoch, AnonID: "anon-1", ProjectBucket: project,
		Package: purl, Stage: domain.Stage(stage), Result: domain.Result(result),
		ErrorCode: code, ErrorFingerprint: fp,
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

// Two versions a month apart are an upgrade, not a collision. A project
// bucket lasts a month, so grouping by bucket alone called every upgrade a
// conflict.
func TestVersionsSeenOnDifferentDaysAreNotACollision(t *testing.T) {
	f := NewFake()
	conflictObsOn(t, f, "pkg:npm/ws@7.5.0", "proj-1", "2026-08-01", "PROJECT_TEST", "FAIL", "ERR_X")
	conflictObsOn(t, f, "pkg:npm/ws@8.19.0", "proj-1", "2026-08-20", "PROJECT_TEST", "PASS", "")

	rows, err := f.VersionConflicts(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("conflicts = %+v, want none: the project upgraded", rows)
	}
}

// A failure with no error code says a build containing this package broke and
// nothing about which package broke it. One tsc failure wrote a FAIL for all
// 412 packages in a lockfile, and reading that as a version conflict turned
// one unattributed failure into six accusations against fs-extra.
func TestUnattributedFailuresAreNotEvidenceOfAConflict(t *testing.T) {
	f := NewFake()
	conflictObsOn(t, f, "pkg:npm/ws@7.5.0", "proj-1", "2026-08-20", "PROJECT_TYPECHECK", "FAIL", "")
	conflictObsOn(t, f, "pkg:npm/ws@8.19.0", "proj-1", "2026-08-20", "PROJECT_TYPECHECK", "FAIL", "")

	rows, err := f.VersionConflicts(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("conflicts = %+v, want none: nobody could name a cause", rows)
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

func strings_Repeat(s string, n int) string { return strings.Repeat(s, n) }
