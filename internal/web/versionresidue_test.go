package web

import (
	"net/http"
	"strings"
	"testing"
)

// The version page used to list every sample published against the version,
// flat: pgx v5.10.0 rendered 128 of them under one heading while its 146
// symbol pages showed almost none. Samples belong to the API they answer
// for, so the version page keeps only what no symbol page will claim.
func TestVersionPageLeavesAttributedSamplesToTheirSymbolPages(t *testing.T) {
	mux, f := newTestMux(t, nil)
	seedPgxSamples(f)

	body := mustGet(t, mux, "/golang/github.com/jackc/pgx/v5/v5.10.0")
	for _, id := range []string{"sha256:aaa1", "sha256:bbb2"} {
		if strings.Contains(body, id) {
			t.Errorf("version page listed %s, which a symbol page answers for", id)
		}
	}
	// The one sample that names no symbol has nowhere else to be read, so
	// dropping it here would publish it into a page nobody can reach.
	if !strings.Contains(body, "sha256:ccc3") {
		t.Error("version page dropped the sample that names no symbol")
	}
}

// Each symbol link carries how many samples answer for it, so the count the
// flat list used to convey survives the move.
func TestVersionPageCountsSamplesPerSymbol(t *testing.T) {
	mux, f := newTestMux(t, nil)
	seedPgxSamples(f)

	body := mustGet(t, mux, "/golang/github.com/jackc/pgx/v5/v5.10.0")
	i := strings.Index(body, `<ul class="symlist`)
	if i < 0 {
		t.Fatal("no symbol list on the version page")
	}
	list := body[i:]
	if n := strings.Count(list, `class="symcount`); n != 2 {
		t.Errorf("symbol sample counts rendered = %d, want 2", n)
	}
}

// The samples the version page stopped showing must be reachable, which is
// the whole justification for moving them.
func TestSymbolPageClaimsQualifiedSampleSymbols(t *testing.T) {
	mux, f := newTestMux(t, nil)
	seedPgxSamples(f)

	body := mustGet(t, mux, "/golang/github.com/jackc/pgx/v5/v5.10.0/Batch")
	if !strings.Contains(body, "sha256:aaa1") {
		t.Error("symbol page missed the sample that names the API by module path")
	}
	if strings.Contains(body, "sha256:bbb2") {
		t.Error("symbol page claimed a sample for a different API")
	}
}

func mustGet(t *testing.T, mux *http.ServeMux, target string) string {
	t.Helper()
	rec := get(t, mux, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", target, rec.Code)
	}
	return rec.Body.String()
}

// seedPgxSamples reproduces the three spellings production actually carries
// for one package, plus a sample that names no API at all.
func seedPgxSamples(f *fakeStore) {
	const eco, name, ver = "golang", "github.com/jackc/pgx/v5", "v5.10.0"
	f.versions[eco+"|"+name] = []string{ver}
	f.symbols[eco+"|"+name+"|"+ver] = []string{"Batch", "Identifier"}
	items := []SampleListItem{
		{SampleID: "sha256:aaa1", Goal: "batch a round trip", Status: "CROSS_PASS",
			License: "MIT-0", CreatedAt: "2026-08-01", Version: ver,
			Symbols: []string{"github.com/jackc/pgx/v5.Batch"}, Kind: "HOW"},
		{SampleID: "sha256:bbb2", Goal: "quote an identifier", Status: "CROSS_PASS",
			License: "MIT-0", CreatedAt: "2026-08-02", Version: ver,
			Symbols: []string{"pgx.Identifier"}, Kind: "HOW"},
		{SampleID: "sha256:ccc3", Goal: "connect with a pool", Status: "CROSS_PASS",
			License: "MIT-0", CreatedAt: "2026-08-03", Version: ver, Kind: "HOW"},
	}
	f.sampleList = append(f.sampleList, items...)
	for _, it := range items {
		f.samplePackages[it.SampleID] = []string{"pkg:" + eco + "/" + name + "@" + ver}
	}
}
