package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestVerificationJobsReasonFilterIsAppliedByTheStore(t *testing.T) {
	var crossID, matrixID string
	srv, store, _ := newTestServer(t, func(d *Deps) {
		var err error
		crossID, err = d.Blobs.Put(t.Context(), bytes.NewBufferString("cross artifact"))
		if err != nil {
			t.Fatal(err)
		}
		matrixID, err = d.Blobs.Put(t.Context(), bytes.NewBufferString("matrix artifact"))
		if err != nil {
			t.Fatal(err)
		}
	})
	ctx := t.Context()
	if _, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: crossID, Reason: "cross"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(ctx, serverstore.JobRow{SampleID: matrixID, Reason: "matrix"}); err != nil {
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
	if len(body.Jobs) != 1 || body.Jobs[0].Reason != "cross" || body.Jobs[0].SampleID != crossID {
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

func TestVerificationJobsSkipMissingBlobsWithoutHeadOfLineStarvation(t *testing.T) {
	var liveID string
	srv, store, _ := newTestServer(t, func(d *Deps) {
		var err error
		liveID, err = d.Blobs.Put(t.Context(), bytes.NewBufferString("live artifact"))
		if err != nil {
			t.Fatal(err)
		}
	})
	for i := 0; i < 105; i++ {
		if _, err := store.CreateJob(t.Context(), serverstore.JobRow{
			SampleID: fmt.Sprintf("sha256:missing-%03d", i), Reason: "cross",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateJob(t.Context(), serverstore.JobRow{SampleID: liveID, Reason: "cross"}); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Jobs []struct {
			SampleID string `json:"sampleId"`
		} `json:"jobs"`
	}
	resp := getJSON(t, srv.URL+"/v1/verification/jobs?reason=cross&limit=1", &body)
	if resp.StatusCode != http.StatusOK || len(body.Jobs) != 1 || body.Jobs[0].SampleID != liveID {
		t.Fatalf("jobs = %+v status=%d, want live artifact behind stale rows", body.Jobs, resp.StatusCode)
	}
	for id := int64(1); id <= 105; id++ {
		job, ok, err := store.Job(t.Context(), id)
		if err != nil || !ok || job.Status != "open" {
			t.Fatalf("missing-blob job %d was mutated: %+v ok=%v err=%v", id, job, ok, err)
		}
	}
}
