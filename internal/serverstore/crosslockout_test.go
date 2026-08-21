package serverstore

import (
	"context"
	"testing"
	"time"
)

// A receipt whose contract never ran says nothing about the sample.
//
// One farm node mounted an empty workspace into its container — systemd's
// PrivateTmp — so every verification died at the first stage:
//
//	{"resolve":"FAIL","compile":"SKIPPED","contract":"SKIPPED","load":"SKIPPED"}
//
// 167 receipts were written that way. The samples were fine: pulled by hand
// and run in the same image under the same constraints, they exit 0.
//
// The infrastructure was fixed and the peer still could not verify any of
// them, because the queue hides a cross job from any peer that has filed a
// receipt for that sample — any receipt at all. Sixteen of seventeen open
// cross jobs were invisible to it and the oldest had sat unclaimed for over
// two hours. Deleting the 167 receipts by hand made them visible within
// minutes, which is first aid rather than a fix.
//
// The rule itself is right: a peer that judged a sample must not judge it
// again and manufacture its own independence. But a verdict and a verifier
// that never got as far as running one are not the same fact.
func TestASkippedContractDoesNotLockAPeerOut(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	const sample = "sha256:aaa"
	const peer = "ed25519:c1973797be207ac4"

	if err := f.SaveSample(ctx, SampleRow{SampleID: sample, Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateJob(ctx, JobRow{SampleID: sample, Reason: "cross"}); err != nil {
		t.Fatal(err)
	}
	// The workspace was empty, so nothing was ever judged.
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "r1", SampleID: sample, PeerID: peer,
		ContractResult: "SKIPPED", CreatedAt: time.Now(),
		ReceiptJSON: `{"stages":{"resolve":"FAIL","compile":"SKIPPED","contract":"SKIPPED"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	jobs, err := f.OpenJobsPage(ctx, "", peer, "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("open cross jobs for this peer = %d, want 1 — its receipt judged nothing", len(jobs))
	}
}

// A peer that actually ran the contract and reached a verdict stays out.
// That exclusion is the only thing a cross pass asserts which a publisher
// cannot manufacture alone.
func TestAContractVerdictStillLocksAPeerOut(t *testing.T) {
	for _, verdict := range []string{"PASS", "FAIL"} {
		f := NewFake()
		ctx := context.Background()
		const sample = "sha256:bbb"
		const peer = "ed25519:judged"

		if err := f.SaveSample(ctx, SampleRow{SampleID: sample, Status: "PUBLISHED"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.CreateJob(ctx, JobRow{SampleID: sample, Reason: "cross"}); err != nil {
			t.Fatal(err)
		}
		if err := f.SaveReceipt(ctx, ReceiptRow{
			ReceiptID: "r-" + verdict, SampleID: sample, PeerID: peer,
			ContractResult: verdict, CreatedAt: time.Now(),
			ReceiptJSON: `{"stages":{"contract":"` + verdict + `"}}`,
		}); err != nil {
			t.Fatal(err)
		}

		jobs, err := f.OpenJobsPage(ctx, "", peer, "cross", "", 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(jobs) != 0 {
			t.Errorf("contract=%s left the job visible to the peer that judged it", verdict)
		}
	}
}

// Another peer is unaffected either way: the exclusion is per peer.
func TestTheLockoutIsPerPeer(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	const sample = "sha256:ccc"

	if err := f.SaveSample(ctx, SampleRow{SampleID: sample, Status: "PUBLISHED"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.CreateJob(ctx, JobRow{SampleID: sample, Reason: "cross"}); err != nil {
		t.Fatal(err)
	}
	if err := f.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "r1", SampleID: sample, PeerID: "ed25519:judged",
		ContractResult: "PASS", CreatedAt: time.Now(),
		ReceiptJSON: `{"stages":{"contract":"PASS"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	jobs, err := f.OpenJobsPage(ctx, "", "ed25519:someone-else", "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs for an uninvolved peer = %d, want 1", len(jobs))
	}
}
