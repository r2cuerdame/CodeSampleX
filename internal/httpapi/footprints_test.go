package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const footprintSample = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func seedFootprintSample(t *testing.T, store *serverstore.Fake) {
	t.Helper()
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: footprintSample, Status: "PUBLISHED",
	}); err != nil {
		t.Fatal(err)
	}
}

func footprintBody(outcome string) map[string]any {
	return map[string]any{
		"schemaVersion": 1,
		"sampleId":      footprintSample,
		"outcome":       outcome,
		"stage":         "build",
		"environment":   map[string]string{"os": "Linux", "runtime": "node", "runtimeVersion": "22.11.0"},
	}
}

// The zero-install loop: a caller with no csx client reports that it ran a
// sample, the server keeps it, and every word of the answer says it is an
// unsigned self-report rather than evidence.
func TestExecutionFootprintIsAcceptedAsAnUnsignedSelfReport(t *testing.T) {
	srv, store, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.PublicURL = "https://csx.example/"
	})
	seedFootprintSample(t, store)

	var resp footprintResponse
	if r := postJSON(t, srv.URL+"/v1/footprints/execution", footprintBody("pass"), &resp); r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", r.StatusCode)
	}
	if resp.Status != "accepted" || resp.Outcome != domain.FootprintPass || resp.Stage != domain.FootprintStageBuild {
		t.Fatalf("response = %+v", resp)
	}
	if resp.EvidenceClass != domain.ClassExecutionFootprint || resp.Signed {
		t.Fatalf("a footprint must be filed as an unsigned EXECUTION_FOOTPRINT: %+v", resp)
	}
	if resp.SampleURL != "https://csx.example/samples/"+footprintSample {
		t.Fatalf("sampleUrl = %q", resp.SampleURL)
	}
	if resp.Footprints.Pass != 1 || resp.Footprints.Total() != 1 {
		t.Fatalf("counts = %+v", resp.Footprints)
	}
	if !strings.Contains(resp.Note, "NOT AS EVIDENCE") || !strings.Contains(resp.Note, "never promote") {
		t.Fatalf("note does not say what a footprint is not: %q", resp.Note)
	}

	// The same source on the same day reporting the same sample and stage
	// again is one event whose outcome was replaced, not a second vote.
	if r := postJSON(t, srv.URL+"/v1/footprints/execution", footprintBody("fail"), &resp); r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", r.StatusCode)
	}
	if resp.Status != "updated" || resp.Footprints.Pass != 0 || resp.Footprints.Fail != 1 || resp.Footprints.Total() != 1 {
		t.Fatalf("second report from one source: %+v", resp)
	}

	rows, err := store.ListExecutionFootprints(context.Background(), 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	// What was stored is the normalized token, never the client address.
	if rows[0].Environment.OS != "linux" || rows[0].Epoch != testNow.Format("2006-01-02") {
		t.Fatalf("stored row = %+v", rows[0])
	}
	if len(rows[0].SourceBucket) != 64 || strings.Contains(rows[0].SourceBucket, "127.0.0.1") || strings.Contains(rows[0].DedupKey, "127.0.0.1") {
		t.Fatalf("the client address leaked into the row: %+v", rows[0])
	}
}

// The endpoint refuses anything that is not a closed-vocabulary token, so it
// can never become the place a path, a log line or a project name is stored.
func TestExecutionFootprintRefusesFreeText(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	seedFootprintSample(t, store)

	for name, mutate := range map[string]func(map[string]any){
		"schema":      func(b map[string]any) { b["schemaVersion"] = 2 },
		"sample":      func(b map[string]any) { b["sampleId"] = "axios-upload" },
		"outcome":     func(b map[string]any) { b["outcome"] = "mostly worked" },
		"stage":       func(b map[string]any) { b["stage"] = "deploy" },
		"path":        func(b map[string]any) { b["environment"] = map[string]string{"os": "C:/Users/me/project"} },
		"log":         func(b map[string]any) { b["environment"] = map[string]string{"runtime": "node v22 (error: ENOENT)"} },
		"fingerprint": func(b map[string]any) { b["failureFingerprint"] = "TypeError: cannot read properties of undefined" },
	} {
		body := footprintBody("pass")
		mutate(body)
		var errResp map[string]string
		r := postJSON(t, srv.URL+"/v1/footprints/execution", body, &errResp)
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, r.StatusCode)
		}
		if errResp["error"] == "" || strings.Contains(errResp["error"], "C:/Users") || strings.Contains(errResp["error"], "ENOENT") {
			t.Errorf("%s: error %q must explain the rule without echoing the value", name, errResp["error"])
		}
	}
	if rows, _ := store.ListExecutionFootprints(context.Background(), 10); len(rows) != 0 {
		t.Fatalf("a refused footprint was stored: %+v", rows)
	}
}

// A footprint about a sample this network never published, or one an
// operator withdrew, is not about anything the server serves.
func TestExecutionFootprintNeedsAServedSample(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	var errResp map[string]string
	if r := postJSON(t, srv.URL+"/v1/footprints/execution", footprintBody("pass"), &errResp); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown sample: status = %d", r.StatusCode)
	}
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID: footprintSample, Status: "PUBLISHED", Quarantined: true,
	}); err != nil {
		t.Fatal(err)
	}
	if r := postJSON(t, srv.URL+"/v1/footprints/execution", footprintBody("pass"), &errResp); r.StatusCode != http.StatusNotFound {
		t.Fatalf("quarantined sample: status = %d", r.StatusCode)
	}
}

// The evidence-strength rule in one number: however many footprints arrive,
// the class contributes nothing to any weighted aggregate.
func TestExecutionFootprintWeighsNothing(t *testing.T) {
	if w := compatibility.ClassWeight(domain.ClassExecutionFootprint); w != 0 {
		t.Fatalf("ClassWeight(EXECUTION_FOOTPRINT) = %v, want 0", w)
	}
	if w := compatibility.ClassWeight(domain.ClassAdoptionEvidence); w <= 0 {
		t.Fatalf("the class above it must still weigh something: %v", w)
	}
}
