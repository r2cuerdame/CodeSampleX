package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// seedChosen records that somebody listed this package in their own manifest
// and that their lockfile resolved it onto the exact children given.
//
// Direct is the flag that separates "somebody chose this" from "somebody
// received this", and it is the anchor the dependency closure hangs from.
func seedChosen(t *testing.T, store expansionStore, name, version string, children ...string) {
	t.Helper()
	seedEdgeBatch(t, store, name, version, true, "proj-"+name+version, 5, children...)
}

// seedCarried is the same sighting without the choice: a package that arrived
// as somebody else's dependency.
func seedCarried(t *testing.T, store expansionStore, name, version string, children ...string) {
	t.Helper()
	seedEdgeBatch(t, store, name, version, false, "proj-"+name+version, 5, children...)
}

func seedEdgeBatch(t *testing.T, store expansionStore, name, version string, direct bool, bucket string, count int, children ...string) {
	t.Helper()
	ctx := context.Background()
	purl := "pkg:npm/" + name + "@" + version
	depends := make([]string, 0, len(children))
	for _, child := range children {
		depends = append(depends, "pkg:npm/"+child)
	}
	b := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "anon-" + name, ProjectBucket: bucket,
		Package: purl, Direct: direct, DependsOn: depends,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: count,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
		},
	}
	accepted, rejected, err := store.IngestBatches(ctx, []domain.ObservationBatch{b})
	if err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", purl, accepted, rejected, err)
	}
	if err := store.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: name, Version: version,
		Major:      domain.PURL{Ecosystem: "npm", Name: name, Version: version}.Major(),
		Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

// dependencyWork lists the DEPENDENCY coordinates the scheduler is offering.
func dependencyWork(t *testing.T, store expansionStore) []WantedRow {
	t.Helper()
	rows, err := store.ListAuthoringExpansionCandidates(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]WantedRow, 0, len(rows))
	for _, r := range rows {
		if r.Kind == "DEPENDENCY" {
			out = append(out, r)
		}
	}
	return out
}

// A dependency nobody ever reported on its own is unreachable by every other
// queue source: each of them starts from an evidence row keyed by the exact
// purl, and a transitive dependency has none. Its column in the compatibility
// matrix therefore stays blank however long the fleet runs, which is the
// backlog R2C-89 exists to drain.
func TestUnobservedDependencyOfAChosenPackageBecomesWork(t *testing.T) {
	f := NewFake()
	seedChosen(t, f, "express", "5.1.0", "body-parser@2.2.0")

	rows := dependencyWork(t, f)
	if len(rows) != 1 {
		t.Fatalf("dependency work = %+v, want one row for body-parser@2.2.0", rows)
	}
	got := rows[0]
	if got.Name != "body-parser" || got.Version != "2.2.0" || got.Ecosystem != "npm" {
		t.Errorf("dependency work = %+v, want npm/body-parser@2.2.0", got)
	}
	if got.Symbol != "" {
		t.Errorf("dependency work carries symbol %q; a resolved edge proves a package, not a symbol", got.Symbol)
	}
	if got.Score < 1 {
		t.Errorf("dependency score = %d, want the project-days that resolved it", got.Score)
	}
}

// A dependency arrives already shredded across every parent that pulled it.
// Offering it once per parent would hand two workers the same coordinate and
// bill the network twice for one answer.
func TestOneDependencyRequiredByTwoParentsIsOneWorkItem(t *testing.T) {
	f := NewFake()
	seedChosen(t, f, "express", "5.1.0", "body-parser@2.2.0")
	seedChosen(t, f, "koa", "3.0.1", "body-parser@2.2.0")

	rows := dependencyWork(t, f)
	if len(rows) != 1 {
		t.Fatalf("dependency work = %+v, want one canonical row", rows)
	}
	if rows[0].Score < 2 {
		t.Errorf("score = %d; two parents resolved it, and the score is what says so", rows[0].Score)
	}
}

// A package nobody chose is a shadow: the network sees it because somebody
// else's lockfile carried it. Anchoring the closure to chosen packages is what
// keeps it from walking the whole registry.
func TestCarriedPackagesDoNotSeedTheClosure(t *testing.T) {
	f := NewFake()
	seedCarried(t, f, "shadow", "1.0.0", "deep@1.0.0")

	if rows := dependencyWork(t, f); len(rows) != 0 {
		t.Errorf("dependency work = %+v, want none from a package nobody chose", rows)
	}
}

// Transitive expansion, and the bound on it. The chain is three deep and each
// level opens only when the level above it becomes an anchor:
//
//	express (chosen)  ->  body-parser  ->  raw-body
//
// body-parser is dependency work while nothing has reported it. Once a machine
// does report it, it is an ordinary observed hole and the package-level branch
// owns it -- but raw-body still does not open, because being carried by
// somebody is not being wanted by anybody. Only proving body-parser opens the
// next level, which is what makes a huge tree cost one verified sample per
// level instead of arriving in one pass.
func TestTheClosureOpensOneLevelPerAnchor(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	seedChosen(t, f, "express", "5.1.0", "body-parser@2.2.0")

	before := dependencyWork(t, f)
	if len(before) != 1 || before[0].Name != "body-parser" {
		t.Fatalf("dependency work = %+v, want body-parser while nothing has reported it", before)
	}

	// A machine that received body-parser reports its own lockfile. That makes
	// body-parser an observed coordinate the existing branches already reach.
	seedCarried(t, f, "body-parser", "2.2.0", "raw-body@3.0.0")
	carried := dependencyWork(t, f)
	if len(carried) != 0 {
		t.Fatalf("dependency work = %+v, want none: body-parser is now observed and raw-body has only a carried parent", carried)
	}

	seedVerifiedSample(t, f, ctx, "pkg:npm/body-parser@2.2.0", "linux", now)

	after := dependencyWork(t, f)
	if len(after) != 1 || after[0].Name != "raw-body" || after[0].Version != "3.0.0" {
		t.Fatalf("dependency work = %+v, want raw-body@3.0.0 once body-parser is proven", after)
	}
}

// A cycle is what a dependency graph does when two packages carry each other.
// Walking it must terminate, and the coordinates it yields must be finite.
func TestDependencyCycleTerminates(t *testing.T) {
	f := NewFake()
	seedChosen(t, f, "alpha", "1.0.0", "beta@1.0.0")
	seedChosen(t, f, "beta", "1.0.0", "alpha@1.0.0")

	rows := dependencyWork(t, f)
	// Both ends are already observed and chosen, so neither is an unobserved
	// dependency and the cycle yields nothing at all.
	if len(rows) != 0 {
		t.Errorf("dependency work = %+v, want none: both ends of the cycle are already observed", rows)
	}
}

// One library resolved at many versions across many projects must not fill the
// whole candidate window. Uncapped it does exactly that, and the fleet then
// reads NO_WORK while real demand sits just outside the limit.
func TestDependencyClosureCapsVersionsOfOnePackage(t *testing.T) {
	f := NewFake()
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "1.4.0", "1.5.0", "1.6.0", "1.7.0"} {
		seedEdgeBatch(t, f, "parent", v, true, "proj-"+v, 5, "tslib@"+v)
	}
	rows := dependencyWork(t, f)
	if len(rows) != authoringSiblingVersionsPerPackage {
		t.Errorf("dependency work = %d rows, want the per-package cap of %d: %+v",
			len(rows), authoringSiblingVersionsPerPackage, rows)
	}
}

// A package the network already proves at some version is version-breadth
// work, not dependency-closure work. Emitting it as both would rank one
// coordinate twice and make the dependency backlog read high for a reason
// that has nothing to do with dependencies.
func TestAProvenPackageNameIsVersionBreadthNotDependencyWork(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	seedChosen(t, f, "express", "5.1.0", "body-parser@2.2.0")
	// A different release of the same dependency is already proven.
	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/body-parser@1.20.0", Ecosystem: "npm", Name: "body-parser",
		Version: "1.20.0", Major: "1", Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	seedVerifiedSample(t, f, ctx, "pkg:npm/body-parser@1.20.0", "linux", now)

	if rows := dependencyWork(t, f); len(rows) != 0 {
		t.Errorf("dependency work = %+v, want none: this package is already proven at another version", rows)
	}
}

// The priority the issue fixes: real demand and measured holes above the
// dependency closure, and the dependency closure above version breadth.
func TestDependencyClosureOutranksVersionBreadth(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	// A proven package, so its unproven siblings become version-breadth work.
	seedChosen(t, f, "left-pad", "1.3.0")
	seedVerifiedSample(t, f, ctx, "pkg:npm/left-pad@1.3.0", "linux", now)
	if err := f.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/left-pad@1.2.0", Ecosystem: "npm", Name: "left-pad",
		Version: "1.2.0", Major: "1", Publicness: "PUBLIC", LastSeen: now,
	}); err != nil {
		t.Fatal(err)
	}
	// A dependency nobody has ever measured.
	seedChosen(t, f, "express", "5.1.0", "body-parser@2.2.0")

	rows, err := f.ListAuthoringExpansionCandidates(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	dependencyAt, siblingAt := -1, -1
	for i, r := range rows {
		if r.Kind == "DEPENDENCY" && r.Name == "body-parser" && dependencyAt < 0 {
			dependencyAt = i
		}
		if r.Kind == "EXPANSION" && r.Name == "left-pad" && r.Version == "1.2.0" && siblingAt < 0 {
			siblingAt = i
		}
	}
	if dependencyAt < 0 || siblingAt < 0 {
		t.Fatalf("expected both a dependency row and a version-breadth row: %+v", rows)
	}
	if dependencyAt > siblingAt {
		t.Errorf("dependency work ranked %d, version breadth ranked %d; the closure must come first", dependencyAt, siblingAt)
	}
}

// depStep is one thing that happened, replayed identically into both stores.
type depStep struct {
	// an observation batch: this package, whether its reporter chose it, and
	// the exact children its lockfile resolved.
	name, version string
	direct        bool
	bucket        string
	children      []string
	// count is this sighting's observation count. Every scenario gives its
	// packages distinct counts on purpose: the last ordering term the two
	// stores share is last_seen, and it is the one term a parity test cannot
	// feed them identically -- PostgreSQL stamps it with now() from inside
	// the query while the Fake reads a clock the test has to pin, and on a
	// Windows timer the Fake's eight in-memory writes all land in one tick.
	// Distinct scores settle the order before recency is ever consulted.
	count int
	// or a verified sample for provenPURL, with a passing receipt from
	// provenOS.
	provenPURL, provenOS string
}

func replayDepSteps(t *testing.T, store expansionStore, steps []depStep) {
	t.Helper()
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for _, s := range steps {
		if s.provenPURL != "" {
			seedVerified(t, store, s.provenPURL, s.provenOS, now)
			continue
		}
		bucket := s.bucket
		if bucket == "" {
			bucket = "proj-" + s.name + s.version
		}
		count := s.count
		if count == 0 {
			count = 5
		}
		seedEdgeBatch(t, store, s.name, s.version, s.direct, bucket, count, s.children...)
	}
}

// seedVerified publishes a sample covering purl with one passing receipt from
// the given OS -- the shape the verifier fleet actually produces -- into
// whichever store it is given.
func seedVerified(t *testing.T, store expansionStore, purl, os string, now time.Time) {
	t.Helper()
	ctx := context.Background()
	sampleID := "sha256:" + purl
	if err := store.SaveSample(ctx, SampleRow{
		SampleID:     sampleID,
		ManifestJSON: `{"packages":["` + purl + `"],"symbols":[],"case":{"goal":"verify ` + purl + `"}}`,
		Status:       "CROSS_PASS", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "r-" + purl, SampleID: sampleID, PeerID: "ed25519:1111111111111111",
		EnvHash: "env-" + purl, ContractResult: "PASS",
		ReceiptJSON: `{"environment":{"os":"` + os + `"}}`,
	}); err != nil {
		t.Fatal(err)
	}
}

// The dependency closure lives in two implementations -- a Go walk over the
// Fake's edge map and a CTE over dependency_edge -- and the repo's own
// comments record what happens when the two halves of this query drift: a
// test proves an assignment the server would never make. So the scenarios
// below are replayed into both stores and the two candidate orders are
// compared row for row, including the non-dependency rows, because adding a
// source rank renumbers every branch below it.
func TestIntegrationDependencyClosureParity(t *testing.T) {
	scenarios := []struct {
		name  string
		steps []depStep
	}{
		{
			name: "an unobserved dependency of a chosen package",
			steps: []depStep{
				{name: "express", version: "5.1.0", direct: true, children: []string{"body-parser@2.2.0"}},
			},
		},
		{
			name: "two parents, one canonical child",
			steps: []depStep{
				{name: "express", version: "5.1.0", direct: true, bucket: "p1", count: 9, children: []string{"body-parser@2.2.0"}},
				{name: "koa", version: "3.0.1", direct: true, bucket: "p2", count: 4, children: []string{"body-parser@2.2.0"}},
			},
		},
		{
			name: "a carried parent anchors nothing",
			steps: []depStep{
				{name: "shadow", version: "1.0.0", direct: false, children: []string{"deep@1.0.0"}},
			},
		},
		{
			name: "proving the middle opens the next level",
			steps: []depStep{
				{name: "express", version: "5.1.0", direct: true, count: 9, children: []string{"body-parser@2.2.0"}},
				{name: "body-parser", version: "2.2.0", direct: false, count: 4, children: []string{"raw-body@3.0.0"}},
				{provenPURL: "pkg:npm/body-parser@2.2.0", provenOS: "linux"},
			},
		},
		{
			name: "one library resolved at more versions than the cap",
			steps: []depStep{
				{name: "parent", version: "1.0.0", direct: true, bucket: "b0", count: 80, children: []string{"tslib@1.0.0"}},
				{name: "parent", version: "1.1.0", direct: true, bucket: "b1", count: 73, children: []string{"tslib@1.1.0"}},
				{name: "parent", version: "1.2.0", direct: true, bucket: "b2", count: 66, children: []string{"tslib@1.2.0"}},
				{name: "parent", version: "1.3.0", direct: true, bucket: "b3", count: 59, children: []string{"tslib@1.3.0"}},
				{name: "parent", version: "1.4.0", direct: true, bucket: "b4", count: 52, children: []string{"tslib@1.4.0"}},
				{name: "parent", version: "1.5.0", direct: true, bucket: "b5", count: 45, children: []string{"tslib@1.5.0"}},
				{name: "parent", version: "1.6.0", direct: true, bucket: "b6", count: 38, children: []string{"tslib@1.6.0"}},
				{name: "parent", version: "1.7.0", direct: true, bucket: "b7", count: 31, children: []string{"tslib@1.7.0"}},
			},
		},
		{
			name: "a scoped npm name on both ends of the edge",
			steps: []depStep{
				{name: "@scope/app", version: "1.0.0", direct: true, children: []string{"@scope/util@2.0.0"}},
			},
		},
		{
			name: "the closure against version breadth and a scored symbol",
			steps: []depStep{
				{name: "left-pad", version: "1.3.0", direct: true, count: 9, children: nil},
				{provenPURL: "pkg:npm/left-pad@1.3.0", provenOS: "linux"},
				{name: "express", version: "5.1.0", direct: true, count: 4, children: []string{"body-parser@2.2.0"}},
			},
		},
		{
			name: "a cycle between two observed packages",
			steps: []depStep{
				{name: "alpha", version: "1.0.0", direct: true, bucket: "c1", count: 9, children: []string{"beta@1.0.0"}},
				{name: "beta", version: "1.0.0", direct: true, bucket: "c2", count: 4, children: []string{"alpha@1.0.0"}},
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := NewFake()
			fake.NowFn = func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) }
			replayDepSteps(t, fake, sc.steps)
			pg := openTestPG(t)
			replayDepSteps(t, pg, sc.steps)

			fakeRows, err := fake.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			pgRows, err := pg.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(fakeRows) != len(pgRows) {
				t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
					len(fakeRows), len(pgRows), formatCandidateOrder(fakeRows), formatCandidateOrder(pgRows))
			}
			for i := range pgRows {
				if got, want := candidateLine(fakeRows[i]), candidateLine(pgRows[i]); got != want {
					t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, got, want)
				}
			}
		})
	}
}

// Producing a dependency candidate is not the same as being able to hand it
// out. 0013 wrote the assignment kind vocabulary as an inline CHECK, so a
// DEPENDENCY claim fails at the INSERT -- and the in-memory store has no
// constraint to violate, so a Fake-only suite would report the whole feature
// working while production refused every job it generated.
func TestIntegrationDependencyWorkCanActuallyBeClaimed(t *testing.T) {
	ctx := context.Background()
	pg := openTestPG(t)
	replayDepSteps(t, pg, []depStep{
		{name: "express", version: "5.1.0", direct: true, count: 9, children: []string{"body-parser@2.2.0"}},
	})
	now := time.Now().UTC()
	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-dep-claim", SessionID: "dep-claimer", Label: "dep-claimer",
		Model: "agy", Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	dependency := make([]WantedRow, 0, 1)
	for _, c := range candidates {
		if c.Kind == "DEPENDENCY" {
			dependency = append(dependency, c)
		}
	}
	if len(dependency) != 1 {
		t.Fatalf("dependency candidates = %+v, want one", dependency)
	}
	work, ok, err := pg.ClaimAuthoringWork(ctx, "dep-claimer", dependency, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("claiming dependency work failed: %v", err)
	}
	if !ok || work.Kind != "DEPENDENCY" || work.Name != "body-parser" {
		t.Fatalf("claim = %+v ok=%v, want the dependency coordinate", work, ok)
	}

	// A second worker must not be handed the same coordinate.
	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{{
		TokenHash: "hash-dep-second", SessionID: "dep-second", Label: "dep-second",
		Model: "agy", Reasoning: "auto", IssuedAt: now, IdleExpiresAt: now.Add(time.Hour),
	}}, now); err != nil {
		t.Fatal(err)
	}
	if second, ok, err := pg.ClaimAuthoringWork(ctx, "dep-second", dependency, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Errorf("a second worker was handed the leased coordinate %+v", second)
	}

	// Attaching a sample releases the package-level slot rather than pinning
	// it: the claim key ignores the symbol, so a row left behind would take
	// the release off the board for good.
	if err := pg.SaveSample(ctx, SampleRow{
		SampleID: "sha256:dep-sample", ManifestJSON: `{"packages":["pkg:npm/body-parser@2.2.0"],"symbols":[]}`,
		Status: "DRAFT", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	attached, err := pg.AttachAuthoringWorkSample(ctx, "dep-claimer", work, "sha256:dep-sample", now)
	if err != nil || !attached {
		t.Fatalf("attach = %v err=%v", attached, err)
	}
	if next, ok, err := pg.ClaimAuthoringWork(ctx, "dep-second", dependency, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	} else if !ok || next.Name != "body-parser" {
		t.Errorf("the package-level slot was not released after the sample was attached: %+v ok=%v", next, ok)
	}
}
