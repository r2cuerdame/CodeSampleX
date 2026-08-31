package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// R2C-134. The census says how many coordinates sit in each of the eight
// states; it cannot say WHICH ones, so nobody could act on it. /gaps is that
// list, and the one property it must hold is that it is the same reading.
//
// A page counting a different set from the census it illustrates reports work
// that never shrinks however hard the farm runs -- the failure the census
// query's own comment was written to prevent. So the invariant is stated as a
// test rather than as a convention: fold the listed rows back into cells and
// the result is the census with SED removed.
func TestCompletenessGapsFoldBackIntoTheCensus(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	for _, c := range []struct {
		name                        string
		sample, evidence, dependent bool
	}{
		{"sed", true, true, true},
		{"se", true, true, false},
		{"ed", false, true, true},
		{"e", false, true, false},
		{"none", false, false, false},
		{"e2", false, true, false},
	} {
		seedCompletenessCoordinate(t, f, c.name, c.sample, c.evidence, c.dependent, now)
	}
	// The two shapes the census treats specially, without which this test
	// passes whatever the admission rule says.
	//
	// A coordinate unaskable on BOTH axes leaves States entirely, so it must
	// leave the listing too; one unaskable on a single axis stays in both.
	// Seeded here because the shared helper only writes npm coordinates, and
	// npm is the one ecosystem that can be scanned.
	seedForeignCoordinate(t, f, "maven", "org.example/plugin.gradle.plugin")
	seedForeignCoordinate(t, f, "maven", "org.example/ordinary")

	census, err := f.FarmCompletenessNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err := f.CompletenessGaps(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != len(rows) {
		t.Errorf("total = %d but %d rows came back", total, len(rows))
	}

	folded := map[string]int{}
	for _, r := range rows {
		if r.State() == "SED" {
			t.Errorf("%s is complete and must not be listed as a gap", r.Name)
		}
		folded[r.State()]++
	}
	for _, state := range completenessStates {
		want := census.States[state]
		if state == "SED" {
			want = 0
		}
		if folded[state] != want {
			t.Errorf("state %s: gaps list %d, census says %d", state, folded[state], want)
		}
	}
}

// The dependency axis keeps its three-way answer all the way to the page.
//
// "A resolver read this and it declared nothing" and "nobody has ever looked"
// are the same blank on screen and opposite facts. Collapsing them here would
// put a closed coordinate back on the work list forever, and would have the
// page assert a measurement that never happened.
func TestCompletenessGapsSeparateProvenNoneFromNeverLooked(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	seedCompletenessCoordinate(t, f, "silent", false, true, false, now)
	seedCompletenessCoordinate(t, f, "leaf", false, true, false, now)
	seedResolvedNone(t, f, "leaf", "1.0.0")

	rows, _, err := f.CompletenessGaps(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name] = r.Dependency
	}
	if got["silent"] != DependencyGapUnknown {
		t.Errorf("a release nobody resolved reads as %q, want %q", got["silent"], DependencyGapUnknown)
	}
	if got["leaf"] != DependencyGapProvenNone {
		t.Errorf("a release measured to declare nothing reads as %q, want %q", got["leaf"], DependencyGapProvenNone)
	}
}

// A gap nobody can close says so, and says why.
//
// The census already subtracts these from the backlog; the page has to show
// the same judgement or a contributor picks up work the queue will decline on
// every poll. The reason is the queue's own sentence, not a second one written
// to look like it.
func TestCompletenessGapsCarryTheReasonAGapCannotBeClosed(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	// An npm per-platform native build: no contract can import it directly.
	seedCompletenessCoordinate(t, f, "@esbuild/linux-x64", false, true, false, now)
	seedCompletenessCoordinate(t, f, "ordinary", false, true, false, now)

	rows, _, err := f.CompletenessGaps(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CompletenessGap{}
	for _, r := range rows {
		got[r.Name] = r
	}
	if got["@esbuild/linux-x64"].SampleNAReason == "" {
		t.Error("a per-platform native build was listed as ordinary missing work")
	}
	if got["ordinary"].SampleNAReason != "" {
		t.Errorf("an ordinary package claims it cannot be sampled: %q",
			got["ordinary"].SampleNAReason)
	}
}

// seedResolvedNone records that a resolver read this exact release and it
// declared nothing, through the same ingest path production uses.
func seedResolvedNone(t *testing.T, store completenessStore, name, version string) {
	t.Helper()
	if _, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-31", AnonID: "anon-none-" + name,
		ProjectBucket: "proj-none-" + name,
		Package:       "pkg:npm/" + name + "@" + version,
		Stage:         domain.StageProjectCompile, Result: domain.ResultPass,
		ObservationCount: 1, Direct: true, DependsOnNone: true,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
			Runtime: "node", RuntimeVersion: "22",
		},
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("seed resolved-none for %s: rejected=%v err=%v", name, rejected, err)
	}
}

// seedForeignCoordinate records one PUBLIC release in an ecosystem the shared
// helper does not reach, with evidence and nothing else.
func seedForeignCoordinate(t *testing.T, store completenessStore, ecosystem, name string) {
	t.Helper()
	const version = "1.0.0"
	if err := store.UpsertPackage(t.Context(), PackageRow{
		PURL:      "pkg:" + ecosystem + "/" + name + "@" + version,
		Ecosystem: ecosystem, Name: name, Version: version,
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

// The two stores list the same gaps, in the same order, with the same reasons.
//
// This is the divergence class that has already shipped twice on this axis:
// the Fake agreed with itself and production quietly said something else. The
// classification lives in SQL for PostgreSQL and in Go for the Fake, so
// nothing but this test holds them to one answer.
func TestIntegrationCompletenessGapsParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	for _, store := range []completenessStore{pg, f} {
		// Every axis combination the corpus actually produces.
		seedCompletenessCoordinate(t, store, "sed", true, true, true, now)
		seedCompletenessCoordinate(t, store, "se", true, true, false, now)
		seedCompletenessCoordinate(t, store, "ed", false, true, true, now)
		seedCompletenessCoordinate(t, store, "e", false, true, false, now)
		seedCompletenessCoordinate(t, store, "none", false, false, false, now)
		// A measured leaf, which must not read as a coordinate nobody looked at.
		seedCompletenessCoordinate(t, store, "leaf", false, true, false, now)
		seedResolvedNone(t, store, "leaf", "1.0.0")
		// Unaskable on one axis, and on both.
		seedForeignCoordinate(t, store, "maven", "org.example/ordinary")
		seedForeignCoordinate(t, store, "maven", "org.example/plugin.gradle.plugin")
		// A scoped npm platform build: sample-unaskable, dependency-askable.
		seedForeignCoordinate(t, store, "npm", "@esbuild/linux-x64")
	}

	ctx := context.Background()
	pgRows, pgTotal, err := pg.CompletenessGaps(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	fakeRows, fakeTotal, err := f.CompletenessGaps(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if pgTotal != fakeTotal {
		t.Errorf("total: pg=%d fake=%d", pgTotal, fakeTotal)
	}
	if len(pgRows) != len(fakeRows) {
		t.Fatalf("rows: pg=%d fake=%d\npg=%+v\nfake=%+v", len(pgRows), len(fakeRows), pgRows, fakeRows)
	}
	for i := range pgRows {
		if pgRows[i] != fakeRows[i] {
			t.Errorf("row %d differs:\n pg  =%+v\n fake=%+v", i, pgRows[i], fakeRows[i])
		}
	}

	// And the same paging window, which is where an ORDER BY that only
	// agrees on the first page hides.
	pgPage, _, err := pg.CompletenessGaps(ctx, "", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	fakePage, _, err := f.CompletenessGaps(ctx, "", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(pgPage) != len(fakePage) {
		t.Fatalf("page rows: pg=%d fake=%d", len(pgPage), len(fakePage))
	}
	for i := range pgPage {
		if pgPage[i] != fakePage[i] {
			t.Errorf("page row %d differs:\n pg  =%+v\n fake=%+v", i, pgPage[i], fakePage[i])
		}
	}
}
