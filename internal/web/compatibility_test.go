package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R2C-137. /records ranked packages by how many snapshot entries they had,
// which is a fact about this network's bookkeeping rather than about the
// package: a library with four hundred entries and no contract outranked one
// with three and a passing one. The collection now says what was established.
func TestTheCollectionShowsWhatWasEstablishedPerPackage(t *testing.T) {
	store := newFakeStore()
	store.packages = []PackageHit{{Ecosystem: "npm", Name: "zod", LatestVersion: "3.23.8", EvidenceCount: 12}}
	store.packageAssets = []PackageAsset{
		{Ecosystem: "npm", Name: "zod", Releases: 4, WithSample: 4, WithDependency: 1},
	}
	body := warmCompat(t, store)

	if !strings.Contains(body, "all 4 releases") {
		t.Error("a fully proven package does not say so")
	}
	if !strings.Contains(body, "1 of 4 releases") {
		t.Error("a partly answered dependency axis is not shown as a ratio")
	}
}

// A ratio, never a flag.
//
// "This package has a sample" is true of one sample across fifty releases, and
// a reader takes it to mean the package is covered. That overstatement is what
// the three-axis census exists to refuse, and a collection that reintroduces
// it undoes the census one row at a time.
func TestOneProvenReleaseIsNotReportedAsAProvenPackage(t *testing.T) {
	store := newFakeStore()
	store.packages = []PackageHit{{Ecosystem: "npm", Name: "big", LatestVersion: "9.0.0"}}
	store.packageAssets = []PackageAsset{
		{Ecosystem: "npm", Name: "big", Releases: 50, WithSample: 1},
	}
	body := warmCompat(t, store)

	if !strings.Contains(body, "1 of 50 releases") {
		t.Error("a package proven at one release of fifty is not shown as a ratio")
	}
	if strings.Contains(body, "all 50 releases") {
		t.Error("one proven release was reported as the whole package")
	}
}

// A rollup that has not been read yet says so, and does not say "none".
//
// The rollup fills on a timer so the page never waits on a corpus scan. The
// cold state is "we have not looked", and rendering it as "no sample" would
// have every package on the site accuse itself of being unproven for the first
// few seconds after a restart -- a measurement nobody made.
func TestAColdRollupSaysUnmeasuredRatherThanNone(t *testing.T) {
	store := newFakeStore()
	store.packages = []PackageHit{{Ecosystem: "npm", Name: "unknown-yet", LatestVersion: "1.0.0"}}
	store.packageAssets = nil
	body := warmCompat(t, store)

	if !strings.Contains(body, "not measured yet") {
		t.Error("a package the rollup has not covered does not say so")
	}
	if strings.Contains(body, "no release yet") {
		t.Error("an unread rollup was rendered as a measured absence")
	}
}

// A package a sample contradicted a belief about carries that on its row.
func TestAPackageWithAFindingSaysSoOnItsRow(t *testing.T) {
	store := newFakeStore()
	store.packages = []PackageHit{
		{Ecosystem: "npm", Name: "axios", LatestVersion: "1.12.0"},
		{Ecosystem: "npm", Name: "quiet", LatestVersion: "1.0.0"},
	}
	store.packageAssets = []PackageAsset{
		{Ecosystem: "npm", Name: "axios", Releases: 1, WithSample: 1},
		{Ecosystem: "npm", Name: "quiet", Releases: 1, WithSample: 1},
	}
	store.derived = []DerivedFinding{{
		Ecosystem: "npm", Subject: "axios@1.12.0",
		Believed: "a belief", Measured: "the contract said otherwise",
		SampleID: "sha256:abc",
	}}
	body := warmCompat(t, store)

	axios := compatRowHTML(t, body, "npm/axios")
	quiet := compatRowHTML(t, body, "npm/quiet")
	if !strings.Contains(axios, "contradicts a belief") {
		t.Error("a package with a derived finding does not say so")
	}
	if strings.Contains(quiet, "contradicts a belief") {
		t.Error("a package with no finding borrowed another package's")
	}
}

// The retired address keeps working, with its query intact.
func TestRecordsRedirectsToCompatibility(t *testing.T) {
	mux := newTestMuxOnly(t)
	rec := get(t, mux, "/records?eco=npm&page=2")
	if rec.Code != 301 {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/compatibility?eco=npm&page=2" {
		t.Errorf("Location = %q, want the same query on /compatibility", loc)
	}
}

// compatRowHTML returns the one rendered row naming this package.
func compatRowHTML(t *testing.T, body, coord string) string {
	t.Helper()
	// Split on the package row tag. Axis items are list items too, but they
	// are "<li class=...", so they do not split here -- while truncating at
	// the first closing tag would cut the row off before its axes, because
	// that closing tag belongs to the first axis.
	for _, row := range strings.Split(body, "<li>")[1:] {
		if end := strings.Index(row, "</ul>"); end >= 0 {
			row = row[:end]
		}
		if strings.Contains(row, ">"+coord+"<") {
			return row
		}
	}
	t.Fatalf("no row for %s", coord)
	return ""
}

// warmCompat renders /compatibility with the rollup already loaded.
//
// The rollup fills on a timer, so a first request renders the cold state by
// design. Prefilling here measures what a reader actually sees a moment later,
// which is what these tests are about; the cold state has its own test.
func warmCompat(t *testing.T, store *fakeStore) string {
	t.Helper()
	s := &site{
		d:    Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()},
		tmpl: parseTemplates(),
	}
	s.refreshPackageAssets(5 * time.Second)
	rows, err := store.DerivedFindings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	derived := make([]finding, 0, len(rows))
	for _, d := range rows {
		derived = append(derived, finding{Ecosystem: d.Ecosystem, Subject: d.Subject,
			Believed: d.Believed, Measured: d.Measured, SampleID: d.SampleID})
	}
	s.derivedCache, s.derivedAt = derived, time.Now()
	s.handAt = time.Now()

	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/compatibility?lang=en", nil)
	rec := httptest.NewRecorder()
	s.records(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	return rec.Body.String()
}
