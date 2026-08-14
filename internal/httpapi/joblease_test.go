package httpapi

import (
	"strings"
	"testing"
	"time"

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

// ClaimJob has always known how to take back a job whose claim outlived
// JobLease. Nothing could reach that path: OpenJobs only ever listed
// status='open', so a peer that claimed a job and then died — crashed,
// upgraded, powered off — held it for good. In production that left 265
// jobs claimed with zero open, behind a queue that reported itself empty.
func TestAJobHeldByADeadPeerReturnsToTheQueue(t *testing.T) {
	_, store, _ := newTestServer(t, nil)
	ctx := t.Context()
	now := testNow
	store.NowFn = func() time.Time { return now }

	id := "sha256:" + strings.Repeat("c4", 32)
	saveSearchable(t, store, id, testManifest())
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	dead := "ed25519:deadbeefdeadbeef"
	if ok, err := store.ClaimJob(ctx, jobID, dead); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	// While the lease holds, nobody else is offered the job.
	if jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", 100); err != nil {
		t.Fatal(err)
	} else if listed(jobs, jobID) {
		t.Error("a live claim was offered to another peer")
	}

	now = now.Add(serverstore.JobLease + time.Minute)
	jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !listed(jobs, jobID) {
		t.Fatal("an expired claim never came back: the queue is stuck for good")
	}
	if ok, err := store.ClaimJob(ctx, jobID, "ed25519:0123456789abcdef"); err != nil || !ok {
		t.Fatalf("reclaim: ok=%v err=%v", ok, err)
	}
}

func listed(jobs []serverstore.JobRow, id int64) bool {
	for _, j := range jobs {
		if j.ID == id {
			return true
		}
	}
	return false
}
