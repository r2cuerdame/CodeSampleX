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
		`aria-labelledby="sample-declared-environment"`,
		`aria-labelledby="sample-verified-environments"`,
		`aria-labelledby="sample-case-heading"`,
		`aria-labelledby="sample-contract-heading"`,
		`class="sample-detail__token-list sample-detail__symbols"`,
		`data-evidence-result="PASS">PASS`,
		`class="sample-detail__receipt-list"`,
		`class="sample-detail__hash dim mono small">ed25519:0011223344556677`,
		`axios.post`,
	} {
		mustContain(t, body, want)
	}

	for _, id := range []string{
		`id="sample-evidence-heading"`,
		`id="sample-declared-environment"`,
		`id="sample-verified-environments"`,
		`id="sample-case-heading"`,
		`id="sample-contract-heading"`,
		`id="sample-files-heading"`,
		`id="sample-origin-heading"`,
		`id="sample-receipts-heading"`,
	} {
		if got := strings.Count(body, id); got != 1 {
			t.Errorf("%s count = %d, want 1", id, got)
		}
	}
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
		`.sample-detail__hash {`,
		`overflow-wrap: anywhere;`,
		`.sample-detail__token-list {`,
		`.sample-detail__env-grid {`,
		`.sample-detail__facts > div { grid-template-columns: 1fr;`,
	} {
		mustContain(t, stylesheet, want)
	}
}
