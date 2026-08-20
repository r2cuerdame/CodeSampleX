package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func coObs(t *testing.T, f *Fake, purl, project, epoch, result, code string, coresident ...string) {
	t.Helper()
	fp := ""
	if result == string(domain.ResultFail) {
		fp = "sha256:" + strings.Repeat("ab", 32)
	} else {
		code = ""
	}
	acc, rej, err := f.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: epoch, AnonID: "anon-1", ProjectBucket: project,
		Package: purl, Stage: domain.StageProjectTest, Result: domain.Result(result),
		ErrorCode: code, ErrorFingerprint: fp, ObservationCount: 1,
		Coresident: coresident,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}})
	if err != nil || acc != 1 {
		t.Fatalf("ingest %s: acc=%d rej=%+v err=%v", purl, acc, rej, err)
	}
}

// The pair comes from the scanner, which held the whole lockfile. The server
// stores it rather than inferring it, and counts distinct projects rather
// than how often anyone rebuilt.
func TestCoresidenceRecordsThePairTheScannerSaw(t *testing.T) {
	f := NewFake()
	coObs(t, f, "pkg:npm/ws@8.19.0", "proj-1", "2026-08-20", "FAIL", "ERR_X", "7.5.0")
	coObs(t, f, "pkg:npm/ws@7.5.0", "proj-1", "2026-08-20", "PASS", "", "8.19.0")

	rows, err := f.VersionCoresidence(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one pair", rows)
	}
	got := rows[0]
	if got.Lower != "7.5.0" || got.Higher != "8.19.0" {
		t.Errorf("pair = %s + %s", got.Lower, got.Higher)
	}
	// One project, however many packages of that resolution reported it.
	if got.Projects != 1 {
		t.Errorf("projects = %d, want 1", got.Projects)
	}
	if got.Failing != 1 {
		t.Errorf("failing = %d, want 1", got.Failing)
	}
}

// Two projects with the same pair are two projects; the same project on two
// days is two sightings and still one project's worth of trouble per day.
func TestCoresidenceCountsProjectsNotRebuilds(t *testing.T) {
	f := NewFake()
	for i := 0; i < 5; i++ {
		coObs(t, f, "pkg:npm/ws@8.19.0", "proj-1", "2026-08-20", "PASS", "", "7.5.0")
	}
	coObs(t, f, "pkg:npm/ws@8.19.0", "proj-2", "2026-08-20", "PASS", "", "7.5.0")

	rows, err := f.VersionCoresidence(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Projects != 2 {
		t.Fatalf("rows = %+v, want one pair seen by two projects", rows)
	}
}

// An unattributed failure says a build containing this package broke and
// nothing about which package broke it, so it is not evidence the versions
// collided.
func TestCoresidenceIgnoresUnattributedFailures(t *testing.T) {
	f := NewFake()
	coObs(t, f, "pkg:npm/ws@8.19.0", "proj-1", "2026-08-20", "FAIL", "", "7.5.0")
	rows, err := f.VersionCoresidence(t.Context(), "npm", "ws")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Failing != 0 {
		t.Fatalf("rows = %+v, want the pair recorded with no failure credited", rows)
	}
}
