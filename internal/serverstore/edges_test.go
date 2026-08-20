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

// The question that matters most is not "who pulled this" but "what shipped
// WITH this": upgrade fastapi and pydantic moves under you, and the version
// that moved is the one that breaks the build. Same table, read from the
// parent end.
func TestDependenciesShowWhatShippedAlongsideEachVersion(t *testing.T) {
	f := NewFake()
	edgeObs(t, f, "pkg:npm/app@1.0.0", "proj-1", "2026-08-20", "pkg:npm/lib@2.0.0")
	edgeObs(t, f, "pkg:npm/app@1.1.0", "proj-1", "2026-08-20", "pkg:npm/lib@3.0.0")
	edgeObs(t, f, "pkg:npm/app@1.1.0", "proj-2", "2026-08-20", "pkg:npm/lib@3.0.0")

	rows, err := f.Dependencies(t.Context(), "npm", "app")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	projects := map[string]int{}
	for _, r := range rows {
		got[r.ParentVersion] = r.ChildName + "@" + r.ChildVersion
		projects[r.ParentVersion] = r.Projects
	}
	if got["1.0.0"] != "lib@2.0.0" {
		t.Errorf("app 1.0.0 shipped with %q, want lib@2.0.0", got["1.0.0"])
	}
	if got["1.1.0"] != "lib@3.0.0" {
		t.Errorf("app 1.1.0 shipped with %q, want lib@3.0.0", got["1.1.0"])
	}
	if projects["1.1.0"] != 2 {
		t.Errorf("app 1.1.0 + lib 3.0.0 seen by %d projects, want 2", projects["1.1.0"])
	}
}
