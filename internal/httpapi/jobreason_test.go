package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestVerificationJobsReasonFilterIsAppliedByTheStore(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := t.Context()
	if _, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: "sha256:cross", Reason: "cross"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: "sha256:matrix", Reason: "matrix"}); err != nil {
		t.Fatal(err)
	}

	var body struct {
		Jobs []struct {
			SampleID string `json:"sampleId"`
			Reason   string `json:"reason"`
		} `json:"jobs"`
	}
	resp := getJSON(t, srv.URL+"/v1/verification/jobs?capability=CONTAINER_RUN&reason=cross&limit=1", &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if len(body.Jobs) != 1 || body.Jobs[0].Reason != "cross" || body.Jobs[0].SampleID != "sha256:cross" {
		t.Fatalf("cross-only jobs = %+v", body.Jobs)
	}

	resp = getJSON(t, srv.URL+"/v1/verification/jobs?reason=create", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid reason status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	var problem map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	if problem["error"] == "" {
		t.Fatal("invalid reason response did not explain the error")
	}
}
