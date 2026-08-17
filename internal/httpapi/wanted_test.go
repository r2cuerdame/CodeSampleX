package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestWantedPreservesVersionAndClosesOnlyOnAnExactAnswer(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	report := map[string]any{
		"schemaVersion": 1,
		"epoch":         "2026-08-13",
		"anonId":        "0123456789abcdef",
		"packages":      []string{"pkg:npm/three@0.180.0"},
		"symbols":       []string{"Texture.transformUv", "CanvasTexture"},
	}
	if resp := postJSON(t, srv.URL+"/v1/wanted", report, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST wanted status = %d", resp.StatusCode)
	}

	items := wantedItems(t, srv.URL)
	if len(items) != 2 || items[0].Version != "0.180.0" {
		t.Fatalf("wanted items = %+v", items)
	}

	// A sample for the same package but another release and API gives the
	// package a useful page; it does not answer this request.
	manifest := `{"packages":["pkg:npm/three@0.179.0"],"symbols":["Vector3.add"]}`
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: "sha256:other", ManifestJSON: manifest, Status: "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
	if got := wantedItems(t, srv.URL); len(got) != 2 {
		t.Fatalf("different version/symbol closed request: %+v", got)
	}

	manifest = `{"packages":["pkg:npm/three@0.180.0"],"symbols":["Texture.transformUv"]}`
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: "sha256:answer", ManifestJSON: manifest, Status: "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
	if got := wantedItems(t, srv.URL); len(got) != 2 {
		t.Fatalf("source-only sample must not close a verified request: %+v", got)
	}
	if err := store.SaveReceipt(context.Background(), serverstore.ReceiptRow{
		ReceiptID: "receipt-answer", SampleID: "sha256:answer", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
			`"resolvedPackages":["pkg:npm/three@0.180.0"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if got := wantedItems(t, srv.URL); len(got) != 1 || got[0].Symbol != "CanvasTexture" {
		t.Fatalf("answer should close only its exact symbol: %+v", got)
	}

	manifest = `{"packages":["pkg:npm/three@0.180.0"],"symbols":["CanvasTexture"]}`
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: "sha256:second-answer", ManifestJSON: manifest, Status: "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(context.Background(), serverstore.ReceiptRow{
		ReceiptID: "receipt-second", SampleID: "sha256:second-answer", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
			`"resolvedPackages":["pkg:npm/three@0.180.0"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	if got := wantedItems(t, srv.URL); len(got) != 0 {
		t.Fatalf("all exactly answered requests should be closed: %+v", got)
	}
}

func TestWantedBatchCountsManyReportsInOneRequest(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	reports := make([]map[string]any, 0, maxWantedBatchReports)
	for i := 0; i < maxWantedBatchReports; i++ {
		reports = append(reports, map[string]any{
			"schemaVersion": 1,
			"epoch":         "2026-08-13",
			"anonId":        fmt.Sprintf("%016x", i),
			"packages":      []string{"pkg:npm/three@0.180.0"},
			"symbols":       []string{"Texture.transformUv"},
		})
	}
	resp := postJSON(t, srv.URL+"/v1/wanted/batches", map[string]any{
		"schemaVersion": 1, "reports": reports,
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d", resp.StatusCode)
	}
	rows, err := store.TopWanted(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Asks != maxWantedBatchReports {
		t.Fatalf("wanted rows = %+v, want one row with %d reports", rows, maxWantedBatchReports)
	}
}

func TestWantedBatchValidatesEveryReportBeforeWriting(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	resp := postJSON(t, srv.URL+"/v1/wanted/batches", map[string]any{
		"schemaVersion": 1,
		"reports": []map[string]any{
			{
				"schemaVersion": 1, "epoch": "2026-08-13", "anonId": "0123456789abcdef",
				"packages": []string{"pkg:npm/three@0.180.0"},
			},
			{
				"schemaVersion": 1, "epoch": "2026-08-13", "anonId": "fedcba9876543210",
				"packages": []string{"not-a-purl"},
			},
		},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("batch status = %d, want 400", resp.StatusCode)
	}
	rows, err := store.TopWanted(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("invalid batch partially wrote rows: %+v", rows)
	}
}

func TestWantedBatchRejectsMoreThanTwentyReports(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	reports := make([]map[string]any, maxWantedBatchReports+1)
	for i := range reports {
		reports[i] = map[string]any{
			"schemaVersion": 1,
			"epoch":         "2026-08-13",
			"anonId":        fmt.Sprintf("%016x", i),
			"packages":      []string{"pkg:npm/three@0.180.0"},
		}
	}
	resp := postJSON(t, srv.URL+"/v1/wanted/batches", map[string]any{
		"schemaVersion": 1, "reports": reports,
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized batch status=%d, want 400", resp.StatusCode)
	}
	rows, err := store.TopWanted(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("oversized batch wrote rows: %+v", rows)
	}
}

func TestWantedRejectsNamesThatCanChangeRegistryURLMeaning(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	for _, purl := range []string{
		"pkg:npm/react?shadow@18.2.0",
		"pkg:npm/react#shadow@18.2.0",
		"pkg:npm/scope/pkg@1.0.0",
		"pkg:golang/github.com/google/uuid?shadow@v1.6.0",
	} {
		resp := postJSON(t, srv.URL+"/v1/wanted", map[string]any{
			"schemaVersion": 1,
			"epoch":         "2026-08-13",
			"anonId":        "0123456789abcdef",
			"packages":      []string{purl},
		}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST %s status = %d, want 400", purl, resp.StatusCode)
		}
	}
	rows, err := store.TopWanted(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("unsafe package names reached Wanted storage: %+v", rows)
	}
}

func wantedItems(t *testing.T, base string) []wantedListItem {
	t.Helper()
	var out struct {
		SchemaVersion int              `json:"schemaVersion"`
		Items         []wantedListItem `json:"items"`
	}
	resp := getJSON(t, base+"/v1/wanted", &out)
	if resp.StatusCode != http.StatusOK || out.SchemaVersion != 1 {
		t.Fatalf("GET wanted status=%d body=%+v", resp.StatusCode, out)
	}
	return out.Items
}
