package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// An operator reading the farm panel could see an open cross queue while
// every worker reported no work, and both numbers were true: the queue held
// jobs no verifier image in this build can run. Waiting does not consume
// them, so they are counted apart from the backlog rather than inside it.
func TestFarmPanelReportsWorkNoVerifierLaneCanRun(t *testing.T) {
	store := serverstore.NewFake()
	now := time.Date(2026, 8, 23, 17, 30, 0, 0, time.UTC)
	ctx := t.Context()
	sampleID := "sha256:" + "ab12"
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: sampleID, ManifestJSON: `{"schemaVersion":1}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: serverstore.JobStatusUnsupported,
		WantEnvJSON: `{"ecosystem":"golang","runtime":"go","runtimeVersion":"1.27"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open",
		WantEnvJSON: `{"ecosystem":"golang","runtime":"go"}`,
	}); err != nil {
		t.Fatal(err)
	}
	mux, secret := withheldMux(t, store, now)

	req := httptest.NewRequest(http.MethodGet, "/admin/api/farm", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Health struct {
			UnsupportedJobs int `json:"unsupportedJobs"`
		} `json:"health"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Health.UnsupportedJobs != 1 {
		t.Fatalf("unsupportedJobs = %d, want 1 (the runnable job must not be counted): %s",
			got.Health.UnsupportedJobs, rec.Body.String())
	}
}
