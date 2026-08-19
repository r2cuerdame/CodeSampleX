package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func rowsFor(name string, versions ...string) []serverstore.WantedRow {
	out := make([]serverstore.WantedRow, 0, len(versions))
	for _, v := range versions {
		out = append(out, serverstore.WantedRow{Ecosystem: "npm", Name: name, Version: v, Kind: "EXPANSION"})
	}
	return out
}

func versionsOf(rows []serverstore.WantedRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Version)
	}
	return out
}

// The candidate window is capped in SQL, which can only order versions as
// strings — so it ranks 7.0.3 above 14.0.1, the exact bug the version list on
// the site was fixed for. Go has a real comparator, and the versions worth
// working on are the ones the site renders: the newest few.
func TestPreferNewestVersionsUsesVersionPrecedenceNotStringOrder(t *testing.T) {
	rows := rowsFor("axios", "7.0.3", "14.0.1", "2.0.0", "9.9.9", "14.0.0", "1.0.0", "13.5.0")
	got := versionsOf(preferNewestVersions(rows, 3))

	newest := map[string]bool{"14.0.1": true, "14.0.0": true, "13.5.0": true}
	for i := 0; i < 3; i++ {
		if !newest[got[i]] {
			t.Errorf("position %d is %s, want one of the three newest; got order %v", i, got[i], got)
		}
	}
}

// Preference, never a filter. A candidate outside the window is the only work
// left when nothing better is claimable, and dropping it would hand the worker
// NO_WORK instead.
func TestPreferNewestVersionsKeepsEveryRow(t *testing.T) {
	rows := rowsFor("axios", "5.0.0", "4.0.0", "3.0.0", "2.0.0", "1.0.0")
	got := preferNewestVersions(rows, 2)
	if len(got) != len(rows) {
		t.Fatalf("kept %d of %d rows", len(got), len(rows))
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.Version] = true
	}
	for _, r := range rows {
		if !seen[r.Version] {
			t.Errorf("version %s was dropped", r.Version)
		}
	}
}

// The window is per package: a package with two releases must not lose its
// place to another package's twenty.
func TestPreferNewestVersionsIsPerPackage(t *testing.T) {
	rows := append(rowsFor("big", "9.0.0", "8.0.0", "7.0.0", "6.0.0"), rowsFor("small", "1.0.0")...)
	got := preferNewestVersions(rows, 2)
	for _, r := range got {
		if r.Name == "small" && r.Version == "1.0.0" {
			return
		}
	}
	t.Errorf("small's only version vanished: %v", got)
}

// Order within the preferred set, and within the rest, is left alone: the
// store already ranked by merit and depth, and this only lifts a band.
func TestPreferNewestVersionsIsStable(t *testing.T) {
	rows := rowsFor("axios", "1.0.0", "9.0.0", "2.0.0", "8.0.0")
	got := versionsOf(preferNewestVersions(rows, 2))
	want := []string{"9.0.0", "8.0.0", "1.0.0", "2.0.0"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}
