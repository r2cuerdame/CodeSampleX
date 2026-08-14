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

	if err := store.CompleteJobsForSample(ctx, id, "ed25519:0123456789abcdef"); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", "", 100)
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
	if jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", "", 100); err != nil {
		t.Fatal(err)
	} else if listed(jobs, jobID) {
		t.Error("a live claim was offered to another peer")
	}

	now = now.Add(serverstore.JobLease + time.Minute)
	jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", "", 100)
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

// A machine that publishes a sample and also runs a verifier used to claim
// its own cross job, file its own receipt, and retire the job having
// cross-verified nothing. The sample then sat at PUBLISHED forever with no
// open job left to explain why. A receipt from the origin proves only that
// the sample still works where it was built.
func TestAPeerCannotCrossVerifyItsOwnSample(t *testing.T) {
	_, store, _ := newTestServer(t, nil)
	ctx := t.Context()

	id := "sha256:" + strings.Repeat("c5", 32)
	saveSearchable(t, store, id, testManifest())
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "open",
	})
	if err != nil {
		t.Fatal(err)
	}

	origin := "ed25519:0000000000000001"
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: "sha256:r1", SampleID: id, PeerID: origin,
		EnvHash: "sha256:e1", ContractResult: "PASS", ReceiptJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}

	// The origin's own receipt must not retire the cross job.
	if err := store.CompleteJobsForSample(ctx, id, origin); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.OpenJobs(ctx, "CONTAINER_RUN", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !listed(jobs, jobID) {
		t.Fatal("the origin's own receipt closed the cross job: nobody will ever verify this sample")
	}

	// And the origin is not offered the job it cannot answer.
	mine, err := store.OpenJobs(ctx, "CONTAINER_RUN", origin, 100)
	if err != nil {
		t.Fatal(err)
	}
	if listed(mine, jobID) {
		t.Error("the origin was offered its own sample to cross-verify")
	}

	// A second peer answers it, and that does close the job.
	other := "ed25519:0000000000000002"
	if err := store.SaveReceipt(ctx, serverstore.ReceiptRow{
		ReceiptID: "sha256:r2", SampleID: id, PeerID: other,
		EnvHash: "sha256:e2", ContractResult: "PASS", ReceiptJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteJobsForSample(ctx, id, other); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.OpenJobs(ctx, "CONTAINER_RUN", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if listed(jobs, jobID) {
		t.Error("a real cross-verification did not close the job")
	}
}
