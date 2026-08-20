package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func edgeObs(t *testing.T, f *Fake, purl, project, epoch string, dependsOn ...string) {
	t.Helper()
	acc, rej, err := f.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: epoch, AnonID: "anon-1", ProjectBucket: project,
		Package: purl, Stage: domain.StageProjectTest, Result: domain.ResultPass,
		ObservationCount: 1, DependsOn: dependsOn,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}})
	if err != nil || acc != 1 {
		t.Fatalf("ingest %s: acc=%d rej=%+v err=%v", purl, acc, rej, err)
	}
}

// "Two versions are installed" is the half nobody can act on. This is the
// other half: a@1.2.0 wanted b@1.9.0 and c@3.0.0 wanted b@2.1.0, so the
// person looking at a broken build knows which dependency to move.
func TestDependantsNameWhoWantedEachVersion(t *testing.T) {
	f := NewFake()
	edgeObs(t, f, "pkg:npm/a@1.2.0", "proj-1", "2026-08-20", "pkg:npm/b@1.9.0")
	edgeObs(t, f, "pkg:npm/c@3.0.0", "proj-1", "2026-08-20", "pkg:npm/b@2.1.0")

	rows, err := f.Dependants(t.Context(), "npm", "b")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, r := range rows {
		got[r.ChildVersion] = append(got[r.ChildVersion], r.ParentName+"@"+r.ParentVersion)
	}
	if want := []string{"a@1.2.0"}; !equalStrings(got["1.9.0"], want) {
		t.Errorf("1.9.0 pulled by %v, want %v", got["1.9.0"], want)
	}
	if want := []string{"c@3.0.0"}; !equalStrings(got["2.1.0"], want) {
		t.Errorf("2.1.0 pulled by %v, want %v", got["2.1.0"], want)
	}
}

// Counts are of projects, not of rebuilds.
func TestDependantsCountProjectsNotRebuilds(t *testing.T) {
	f := NewFake()
	for i := 0; i < 4; i++ {
		edgeObs(t, f, "pkg:npm/a@1.2.0", "proj-1", "2026-08-20", "pkg:npm/b@1.9.0")
	}
	edgeObs(t, f, "pkg:npm/a@1.2.0", "proj-2", "2026-08-20", "pkg:npm/b@1.9.0")

	rows, err := f.Dependants(t.Context(), "npm", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Projects != 2 {
		t.Fatalf("rows = %+v, want one edge seen by two projects", rows)
	}
}

func equalStrings(a, b []string) bool {
	return strings.Join(a, "|") == strings.Join(b, "|")
}
