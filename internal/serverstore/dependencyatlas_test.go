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
	for _, store := range []Store{pg, f} {
		seedAtlas(t, store)
		// The three shapes the first version of this fixture never had, each
		// of which hid a divergence: one edge seen on several project-days
		// (the Fake counted the edge, PostgreSQL counted the days), a name
		// holding a LIKE metacharacter (PostgreSQL read it as a pattern, the
		// Fake as text), and one name at one version in two ecosystems (the
		// order was not total, so a page boundary could duplicate or drop it).
		atlasEdge(t, store, "p9", "express", "5.1.0", "raw-body", "3.0.0")
		atlasEdge(t, store, "p1", "app", "1.0.0", "a_c", "1.0.0")
		atlasEdge(t, store, "p2", "app", "1.0.0", "abc", "2.0.0")
		atlasEdge(t, store, "p1", "app", "1.0.0", "shared", "1.0.0")
		edgeIn(t, store, "pypi", "p2", "pyapp", "1.0.0", "shared", "1.0.0")
	}
	ctx := context.Background()

	for _, query := range []string{"", "body-parser", "raw", "a_c", "%", "_", "nothing-matches"} {
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

// "Read and found empty" must answer the same in both stores.
//
// It is the one dependency fact that is an ANSWER rather than a count, and the
// page renders it as prose rather than a number — so a disagreement here shows
// up as one store telling a reader a release declares nothing while the other
// leaves its axis open.
func TestIntegrationResolvedNoneParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	ctx := context.Background()

	// A release a resolution read and found empty, in both stores.
	leaf := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-31", AnonID: "anon-leaf", ProjectBucket: "p1",
		Package: "pkg:npm/left-pad@1.3.0", Direct: true, DependsOnNone: true,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22", ModuleSystem: "esm",
		},
	}
	for _, store := range []Store{pg, f} {
		accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{leaf})
		if err != nil || accepted != 1 || len(rejected) != 0 {
			t.Fatalf("ingest: accepted=%d rejected=%v err=%v", accepted, rejected, err)
		}
	}

	for _, c := range []struct {
		name, version string
		want          bool
	}{
		{"left-pad", "1.3.0", true},
		{"left-pad", "9.9.9", false},
		{"never-seen", "1.0.0", false},
	} {
		got, err := pg.DependencyResolvedNone(ctx, "npm", c.name, c.version)
		if err != nil {
			t.Fatal(err)
		}
		fake, err := f.DependencyResolvedNone(ctx, "npm", c.name, c.version)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want || fake != c.want {
			t.Errorf("%s@%s: pg=%v fake=%v want=%v", c.name, c.version, got, fake, c.want)
		}
	}
}

// One edge seen on several project-days counts as several.
//
// The Fake incremented once per edge key while PostgreSQL counted rows, and
// dependency_edge's key includes bucket and epoch — so a row IS a project-day.
// The two stores therefore ranked the same corpus differently, and the atlas
// is ordered by exactly this number.
//
// The first parity fixture never caught it because every edge in it was seen
// once.
func TestOneEdgeOnSeveralProjectDaysCountsAsSeveral(t *testing.T) {
	f := NewFake()
	atlasEdge(t, f, "p1", "express", "5.1.0", "body-parser", "2.2.0")
	atlasEdge(t, f, "p2", "express", "5.1.0", "body-parser", "2.2.0")
	atlasEdge(t, f, "p3", "express", "5.1.0", "body-parser", "2.2.0")

	got, _, err := f.DependencySubjects(context.Background(), "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d subjects, want 1: %v", len(got), atlasLines(got))
	}
	if got[0].Projects != 3 {
		t.Errorf("projects = %d, want 3: one edge on three project-days is three", got[0].Projects)
	}
	if got[0].Parents != 1 {
		t.Errorf("parents = %d, want 1: it is the same parent release each time", got[0].Parents)
	}
}

// A search is over text, so the characters in it are text.
//
// PostgreSQL built the pattern as '%' || $1 || '%' and passed it to ILIKE, so
// a query containing % or _ became a wildcard: "a_c" matched "abc", and
// "%" matched everything. The Fake used strings.Contains and matched neither.
// Two stores, two answers, and the PostgreSQL one is also the wrong answer —
// nobody typing an underscore into a search box means "any character".
func TestASearchTreatsWildcardCharactersAsText(t *testing.T) {
	f := NewFake()
	atlasEdge(t, f, "p1", "app", "1.0.0", "a_c", "1.0.0")
	atlasEdge(t, f, "p2", "app", "1.0.0", "abc", "2.0.0")

	// The underscore is a character, not "any character".
	got, total, err := f.DependencySubjects(context.Background(), "a_c", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].Name != "a_c" {
		t.Errorf("query \"a_c\" matched %d subjects %v, want only a_c", total, atlasLines(got))
	}
	// And a bare percent matches a name containing one, not every name.
	if _, total, err = f.DependencySubjects(context.Background(), "%", 0, 50); err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Errorf("query \"%%\" matched %d subjects; it is a character no name here contains", total)
	}
}

// Two ecosystems can hold the same name at the same version, and the page
// order has to be total.
//
// Ordering ended at (projects, parents, name, version), so two such rows were
// tied and PostgreSQL could return them in either order between one page and
// the next — the row that lands on a page boundary is then shown twice or not
// at all.
func TestTheSubjectOrderIsTotalAcrossEcosystems(t *testing.T) {
	f := NewFake()
	atlasEdge(t, f, "p1", "app", "1.0.0", "shared", "1.0.0")
	// The same child name and version, reached through a pypi parent.
	edgeIn(t, f, "pypi", "p2", "pyapp", "1.0.0", "shared", "1.0.0")

	first, _, err := f.DependencySubjects(context.Background(), "", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := f.DependencySubjects(context.Background(), "", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("paging returned %d and %d rows", len(first), len(second))
	}
	if first[0].Ecosystem == second[0].Ecosystem {
		t.Errorf("both pages returned the %s row; the order is not total", first[0].Ecosystem)
	}
}

// edgeIn is atlasEdge for an ecosystem other than npm.
func edgeIn(t *testing.T, store Store, ecosystem, bucket, parent, parentVersion, child, childVersion string) {
	t.Helper()
	b := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-31", AnonID: "anon-" + parent,
		ProjectBucket: bucket,
		Package:       "pkg:" + ecosystem + "/" + parent + "@" + parentVersion,
		Direct:        true,
		DependsOn:     []string{"pkg:" + ecosystem + "/" + child + "@" + childVersion},
		Stage:         domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: ecosystem, OS: "linux", Arch: "amd64",
			Runtime: "python", RuntimeVersion: "3.12",
		},
	}
	accepted, rejected, err := store.IngestBatches(context.Background(), []domain.ObservationBatch{b})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", ecosystem, accepted, rejected, err)
	}
}
