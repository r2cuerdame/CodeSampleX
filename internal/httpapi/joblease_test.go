package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Claiming a verification job needs no authentication, deliberately:
// publishing here is anonymous and accounts are not the answer. But a claim
// used to be permanent and nothing ever completed one — CompleteJob had no
// route to it — so one stranger could claim every open job and empty the
// verification queue for good, at no cost.
func TestAReceiptClosesTheJobItAnswered(t *testing.T) {
	_, store, _ := newTestServer(t, nil)
	ctx := t.Context()

	id := "sha256:" + strings.Repeat("c3", 32)
	saveSearchable(t, store, id, testManifest())
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.ClaimJob(ctx, jobID, "ed25519:0123456789abcdef"); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	if err := store.CompleteJobsForSample(ctx, id); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.ID == jobID {
			t.Error("the job is still outstanding after its receipt arrived")
		}
	}
}
