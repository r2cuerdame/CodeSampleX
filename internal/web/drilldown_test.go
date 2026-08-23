package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func leafFacts() []cubeFact {
	return []cubeFact{
		{Dims: map[string]string{"version": "1.2.0", "symbol": "axios.get", "os": "linux"},
			Agg: pivotAgg{}},
		{Dims: map[string]string{"version": "1.2.0", "symbol": cubePackageLevel, "os": "linux"},
			Agg: pivotAgg{}, PackageLevel: true},
	}
}

// The cube's leaf is where the drill-down bottoms out, and it is the state a
// reader reaches after pinning every dimension. Today it offers a symbol link
// and nothing else -- and nothing at all for a package-level row -- so the
// most engaged reader on the site arrives at the deepest node with no way
// further down. The version page below it is the one that lists contract
// records.
func TestCubeLeafOffersAWayDownToTheVersion(t *testing.T) {
	rows := cubeLeafRows(leafFacts(), "npm", "axios", "en")
	if len(rows) == 0 {
		t.Fatal("no leaf rows built")
	}
	for i, row := range rows {
		if row.VersionHref == "" {
			t.Errorf("leaf row %d (%s@%s) has no link to its version", i, row.Symbol, row.Version)
			continue
		}
		if !strings.Contains(row.VersionHref, "/npm/axios/1.2.0") {
			t.Errorf("leaf row %d version link = %q, want the version page", i, row.VersionHref)
		}
	}
}

// A package-level leaf row carries no symbol, so before this it emitted no
// link whatsoever. It must still descend.
func TestCubeLeafPackageLevelRowStillDescends(t *testing.T) {
	rows := cubeLeafRows(leafFacts(), "npm", "axios", "en")
	found := false
	for _, row := range rows {
		// The aggregate is called by the package's own name on screen — a
		// reader on this page knows which package they are on and read one
		// generic phrase among a column of API names. It is still the row
		// with no symbol link.
		if row.SymbolHref != "" {
			continue
		}
		found = true
		if row.VersionHref == "" {
			t.Error("package-level leaf row is a dead end")
		}
	}
	if !found {
		t.Fatal("fixture produced no package-level row")
	}
}

// The symbol page is the deepest node in the hierarchy and the only in-content
// link it emitted was an in-page anchor. A reader who followed the cube all
// the way down arrived at the exact API, in the exact environment, and then had
// to climb back up the breadcrumb to reach a single contract record — the thing
// the whole descent was for.
func TestSymbolPageLinksTheContractRecordsThatAnswerIt(t *testing.T) {
	const purl = "pkg:npm/axios@1.12.0"
	mux, store := newTestMux(t, nil)
	store.snapshots[snapKey(purl, "axios.get")] =
		cubeSnap(purl, "axios.get", "linux", "amd64", "node", "22", "npm", "PROJECT_COMPILE", 4, 0)
	store.sampleList = []SampleListItem{
		{SampleID: "sha256:answers", Goal: "retry with axios.get", Version: "1.12.0",
			Symbols: []string{"axios.get"}, Kind: "HOW", CreatedAt: "2026-08-01"},
		{SampleID: "sha256:elsewhere", Goal: "post a form", Version: "1.12.0",
			Symbols: []string{"axios.post"}, Kind: "HOW", CreatedAt: "2026-08-02"},
		{SampleID: "sha256:otherversion", Goal: "older get", Version: "1.11.0",
			Symbols: []string{"axios.get"}, Kind: "HOW", CreatedAt: "2026-08-03"},
	}
	store.samplePackages = map[string][]string{
		"sha256:answers":      {purl},
		"sha256:elsewhere":    {purl},
		"sha256:otherversion": {"pkg:npm/axios@1.11.0"},
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/npm/axios/1.12.0/axios.get", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/samples/sha256:answers") {
		t.Error("the symbol page does not link the record that answers this symbol")
	}
	if strings.Contains(body, "/samples/sha256:elsewhere") {
		t.Error("a record for a different symbol was listed")
	}
	if strings.Contains(body, "/samples/sha256:otherversion") {
		t.Error("a record for a different version was listed")
	}
}
