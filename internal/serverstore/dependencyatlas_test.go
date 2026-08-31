package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// atlasEdge records one parent release pulling one child release, in whichever
// store it is handed.
func atlasEdge(t *testing.T, store Store, bucket, parent, parentVersion, child, childVersion string) {
	t.Helper()
	b := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-31", AnonID: "anon-" + parent,
		ProjectBucket: bucket,
		Package:       "pkg:npm/" + parent + "@" + parentVersion,
		Direct:        true,
		DependsOn:     []string{"pkg:npm/" + child + "@" + childVersion},
		Stage:         domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22", ModuleSystem: "esm",
		},
	}
	accepted, rejected, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{b})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest %s@%s: accepted=%d rejected=%v err=%v", parent, parentVersion, accepted, rejected, err)
	}
}

// seedAtlas writes the same small graph into a store: one child pulled by two
// different parent releases across two projects, one pulled by a single
// parent, and one whose name shares a prefix so a search has something to
// exclude.
func seedAtlas(t *testing.T, store Store) {
	t.Helper()
	atlasEdge(t, store, "p1", "express", "5.1.0", "body-parser", "2.2.0")
	atlasEdge(t, store, "p2", "koa", "3.0.1", "body-parser", "2.2.0")
	atlasEdge(t, store, "p1", "express", "5.1.0", "raw-body", "3.0.0")
	atlasEdge(t, store, "p3", "fastify", "5.0.0", "body-parser-json", "1.0.0")
}

func atlasLines(subjects []DependencySubject) []string {
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, fmt.Sprintf("%s/%s@%s parents=%d projects=%d",
			s.Ecosystem, s.Name, s.Version, s.Parents, s.Projects))
	}
	return out
}

func parentLines(edges []DependencyEdge) []string {
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, fmt.Sprintf("%s@%s -> %s@%s projects=%d",
			e.ParentName, e.ParentVersion, e.ChildName, e.ChildVersion, e.Projects))
	}
	return out
}

// The atlas reads the graph from the child's side, which is the side that had
// no entry point: Dependencies answers "what did this pull" and needs a parent
// named up front, so "who pulls this" could not be asked at all.
func TestTheAtlasRanksSubjectsByHowWidelyTheyWereResolved(t *testing.T) {
	f := NewFake()
	seedAtlas(t, f)

	got, total, err := f.DependencySubjects(context.Background(), "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3 distinct child releases", total)
	}
	want := []string{
		"npm/body-parser@2.2.0 parents=2 projects=2",
		"npm/body-parser-json@1.0.0 parents=1 projects=1",
		"npm/raw-body@3.0.0 parents=1 projects=1",
	}
	if diff := strings.Join(atlasLines(got), "\n"); diff != strings.Join(want, "\n") {
		t.Errorf("subjects:\n got:\n%s\nwant:\n%s", diff, strings.Join(want, "\n"))
	}
}

// Two parent RELEASES is a different fact from two parent names, and it is the
// one worth ranking on. The name count is recoverable from the parent list;
// the release count is not.
func TestTheAtlasCountsParentReleasesNotParentNames(t *testing.T) {
	f := NewFake()
	atlasEdge(t, f, "p1", "express", "5.1.0", "body-parser", "2.2.0")
	atlasEdge(t, f, "p2", "express", "4.18.2", "body-parser", "2.2.0")

	got, _, err := f.DependencySubjects(context.Background(), "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d subjects, want 1: %v", len(got), atlasLines(got))
	}
	if got[0].Parents != 2 {
		t.Errorf("parents = %d, want 2: two releases of one library are two parents", got[0].Parents)
	}
}

// The search matches the child name and nothing else. Matching versions too
// would let "1.0" pull in every package with such a release — noise wearing a
// search box.
func TestTheAtlasSearchMatchesTheNameOnly(t *testing.T) {
	f := NewFake()
	seedAtlas(t, f)

	got, total, err := f.DependencySubjects(context.Background(), "body-parser", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 (body-parser and body-parser-json)", total)
	}
	for _, s := range got {
		if !strings.Contains(s.Name, "body-parser") {
			t.Errorf("%s matched a search for body-parser", s.Name)
		}
	}

	if _, total, err = f.DependencySubjects(context.Background(), "2.2.0", 0, 50); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("a version string matched %d subjects; the search is over names", total)
	}
}

// Both ends of a parent row carry an exact version. "Something in this library
// pulled something in that one" is not a fact anyone can act on, and the
// version that moved under an upgrade is the whole question.
func TestParentsAreListedAtExactVersions(t *testing.T) {
	f := NewFake()
	seedAtlas(t, f)

	got, err := f.DependencyParents(context.Background(), "npm", "body-parser", "2.2.0")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"express@5.1.0 -> body-parser@2.2.0 projects=1",
		"koa@3.0.1 -> body-parser@2.2.0 projects=1",
	}
	if diff := strings.Join(parentLines(got), "\n"); diff != strings.Join(want, "\n") {
		t.Errorf("parents:\n got:\n%s\nwant:\n%s", diff, strings.Join(want, "\n"))
	}

	// A version nothing resolved onto has no parents, and saying so is not the
	// same as saying it has none.
	if got, err := f.DependencyParents(context.Background(), "npm", "body-parser", "9.9.9"); err != nil {
		t.Fatal(err)
	} else if len(got) != 0 {
		t.Errorf("a release nothing pulled reported %d parents", len(got))
	}
}

// The two stores must answer identically.
//
// A browse surface that disagrees with the store behind it is the failure this
// project keeps finding in production rather than in tests: the Fake said one
// thing in every unit test while PostgreSQL said another to every reader.
func TestIntegrationDependencyAtlasParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	seedAtlas(t, pg)
	seedAtlas(t, f)
	ctx := context.Background()

	for _, query := range []string{"", "body-parser", "raw", "nothing-matches"} {
		pgRows, pgTotal, err := pg.DependencySubjects(ctx, query, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		fakeRows, fakeTotal, err := f.DependencySubjects(ctx, query, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		if pgTotal != fakeTotal {
			t.Errorf("query %q: total pg=%d fake=%d", query, pgTotal, fakeTotal)
		}
		if a, b := strings.Join(atlasLines(pgRows), "\n"), strings.Join(atlasLines(fakeRows), "\n"); a != b {
			t.Errorf("query %q disagrees:\n pg:\n%s\n fake:\n%s", query, a, b)
		}
	}

	// Paging has to agree too: an off-by-one in either store silently drops or
	// repeats a row at every page boundary, which no single-page test sees.
	for _, offset := range []int{0, 1, 2, 3, 9} {
		pgRows, _, err := pg.DependencySubjects(ctx, "", offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		fakeRows, _, err := f.DependencySubjects(ctx, "", offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := strings.Join(atlasLines(pgRows), "\n"), strings.Join(atlasLines(fakeRows), "\n"); a != b {
			t.Errorf("offset %d disagrees:\n pg:\n%s\n fake:\n%s", offset, a, b)
		}
	}

	for _, c := range []struct{ name, version string }{
		{"body-parser", "2.2.0"},
		{"raw-body", "3.0.0"},
		{"body-parser", "9.9.9"},
	} {
		pgRows, err := pg.DependencyParents(ctx, "npm", c.name, c.version)
		if err != nil {
			t.Fatal(err)
		}
		fakeRows, err := f.DependencyParents(ctx, "npm", c.name, c.version)
		if err != nil {
			t.Fatal(err)
		}
		if a, b := strings.Join(parentLines(pgRows), "\n"), strings.Join(parentLines(fakeRows), "\n"); a != b {
			t.Errorf("%s@%s disagrees:\n pg:\n%s\n fake:\n%s", c.name, c.version, a, b)
		}
	}
}
