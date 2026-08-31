package serverstore

import (
	"context"
	"testing"
	"time"
)

// R2C-137. /records ranked packages by how many snapshot entries they had,
// which is a fact about this network's bookkeeping and not about the package:
// a library with 400 entries and no sample is less proven than one with 3
// entries and a passing contract.
//
// The rollup is a ratio rather than a flag. "This package has a sample" is
// true of a package with one sample and fifty releases, and reading it as
// "this package is covered" is exactly the overstatement the census exists to
// prevent.
func TestPackageAssetsCountReleasesNotEntries(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	// One package, three releases, one of them proven.
	seedPackageRelease(t, f, "npm", "widget", "1.0.0")
	seedPackageRelease(t, f, "npm", "widget", "1.1.0")
	seedCompletenessCoordinate(t, f, "widget-proven", true, true, false, now)

	rows, err := f.PackageAssets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PackageAsset{}
	for _, r := range rows {
		got[r.Ecosystem+"/"+r.Name] = r
	}
	w := got["npm/widget"]
	if w.Releases != 2 {
		t.Errorf("widget has %d releases, want 2", w.Releases)
	}
	if w.WithSample != 0 {
		t.Errorf("widget reports %d proven releases, want 0", w.WithSample)
	}
	p := got["npm/widget-proven"]
	if p.Releases != 1 || p.WithSample != 1 {
		t.Errorf("widget-proven = %d/%d releases proven, want 1/1", p.WithSample, p.Releases)
	}
}

// A resolved dependency answer counts however it was answered.
//
// A release a resolver read and found empty is answered on that axis, exactly
// like one whose children were recorded. Counting only the graphs would report
// a closed coordinate as outstanding work forever -- the same collapse the
// census keeps three states apart to avoid.
func TestPackageAssetsCountBothKindsOfDependencyAnswer(t *testing.T) {
	f := NewFake()
	ctx := t.Context()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	seedCompletenessCoordinate(t, f, "hasgraph", false, true, true, now)
	seedCompletenessCoordinate(t, f, "isleaf", false, true, false, now)
	seedResolvedNone(t, f, "isleaf", "1.0.0")
	seedCompletenessCoordinate(t, f, "unread", false, true, false, now)

	rows, err := f.PackageAssets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PackageAsset{}
	for _, r := range rows {
		got[r.Name] = r
	}
	for _, tc := range []struct {
		name string
		want int
	}{{"hasgraph", 1}, {"isleaf", 1}, {"unread", 0}} {
		if got[tc.name].WithDependency != tc.want {
			t.Errorf("%s: %d releases answered on the dependency axis, want %d",
				tc.name, got[tc.name].WithDependency, tc.want)
		}
	}
}

// seedPackageRelease records one PUBLIC release and nothing else about it.
func seedPackageRelease(t *testing.T, store completenessStore, ecosystem, name, version string) {
	t.Helper()
	if err := store.UpsertPackage(t.Context(), PackageRow{
		PURL:      "pkg:" + ecosystem + "/" + name + "@" + version,
		Ecosystem: ecosystem, Name: name, Version: version,
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

// Both stores roll up the same way.
//
// The classification is SQL's for PostgreSQL and Go's for the Fake, and this
// axis has already shipped two divergences that every unit test passed.
func TestIntegrationPackageAssetsParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	for _, store := range []completenessStore{pg, f} {
		// Two releases of one package, one proven; plus each dependency answer.
		seedPackageRelease(t, store, "npm", "widget", "1.0.0")
		seedPackageRelease(t, store, "npm", "widget", "1.1.0")
		seedCompletenessCoordinate(t, store, "proven", true, true, false, now)
		seedCompletenessCoordinate(t, store, "hasgraph", false, true, true, now)
		seedCompletenessCoordinate(t, store, "isleaf", false, true, false, now)
		seedResolvedNone(t, store, "isleaf", "1.0.0")
		seedForeignCoordinate(t, store, "maven", "org.example/ordinary")
	}

	pgRows, err := pg.PackageAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	fakeRows, err := f.PackageAssets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pgRows) != len(fakeRows) {
		t.Fatalf("rows: pg=%d fake=%d\npg=%+v\nfake=%+v", len(pgRows), len(fakeRows), pgRows, fakeRows)
	}
	for i := range pgRows {
		if pgRows[i] != fakeRows[i] {
			t.Errorf("row %d differs:\n pg  =%+v\n fake=%+v", i, pgRows[i], fakeRows[i])
		}
	}
}
