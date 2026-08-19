package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestSampleDetailUsesEvidenceHierarchy(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`<article class="sample-detail">`,
		`aria-labelledby="sample-evidence-heading"`,
		`<dl class="sample-detail__metrics">`,
		`class="declared"`,
		`<table class="runs">`,
		`aria-labelledby="sample-case-heading"`,
		`aria-labelledby="sample-contract-heading"`,
		`class="sample-detail__token-list sample-detail__symbols"`,
		`data-evidence-result="PASS">PASS`,
		`class="runpeer dim mono small">ed25519:0011223344556677`,
		`axios.post`,
		// The path back up through the package this sample is about.
		`<nav class="crumbs mono" aria-label="breadcrumb">`,
		`href="/npm/axios"`,
	} {
		mustContain(t, body, want)
	}

	// Every verification run appears once. The page used to print the
	// receipts twice — as an environment list inside the evidence card and
	// again as a receipt list at the bottom — so a reader counting runs
	// counted double.
	if got := strings.Count(body, `data-evidence-result=`); got != len(store(t).receipts["sha256:d1e2f3"]) {
		t.Errorf("verification results rendered %d times, want one per receipt", got)
	}

	for _, id := range []string{
		`id="sample-evidence-heading"`,
		`id="sample-case-heading"`,
		`id="sample-contract-heading"`,
		`id="sample-files-heading"`,
		`id="sample-origin-heading"`,
	} {
		if got := strings.Count(body, id); got != 1 {
			t.Errorf("%s count = %d, want 1", id, got)
		}
	}
}

// store is the shared fixture, for assertions that need to count against
// what the page was given.
func store(t *testing.T) *fakeStore {
	t.Helper()
	return newFakeStore()
}

func TestSampleDetailKeepsContentAddressesWrappable(t *testing.T) {
	mux, store := newTestMux(t, nil)
	fullID := "sha256:" + strings.Repeat("a", 64)
	meta := store.samples["sha256:d1e2f3"]
	meta.SampleID = fullID
	store.samples[fullID] = meta
	store.receipts[fullID] = store.receipts["sha256:d1e2f3"]

	rec := get(t, mux, "/samples/"+fullID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `class="dim mono small sample-id">`+fullID)
	mustContain(t, body, `href="/v1/samples/`+fullID+`/artifact"`)

	css := get(t, mux, "/static/site.css")
	if css.Code != http.StatusOK {
		t.Fatalf("css status %d", css.Code)
	}
	stylesheet := css.Body.String()
	for _, want := range []string{
		`overflow-wrap: anywhere;`,
		`.sample-detail__token-list {`,
		`table.runs {`,
		`.declared {`,
		`.sample-detail__facts > div { grid-template-columns: 1fr;`,
	} {
		mustContain(t, stylesheet, want)
	}
}
