package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A run the sandbox killed at the stage budget measured nothing, and a
// sample that keeps being killed keeps costing five minutes of a verifier to
// learn that again.
//
// Measured on production 2026-09-01: seven samples hold two or more receipts
// whose contract was killed by the timeout, all golang, four of them having
// spent the full four-attempt budget. Their end state is an authoring draft
// that looks exactly like one nothing has got to yet, which is the part that
// is wrong -- the network HAS got to it, four times, and has nothing to say.
func TestRepeatedTimeoutsAreRecordedRatherThanLeftSilent(t *testing.T) {
	timeout := func() serverstore.ReceiptRow {
		return serverstore.ReceiptRow{
			ContractResult: "FAIL",
			ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"FAIL"},` +
				`"stageFailures":{"contract":{"terminationKind":"timeout","timeoutMillis":300000,` +
				`"evidenceQuality":"complete"}}}`,
		}
	}
	judged := func(result, term string) serverstore.ReceiptRow {
		row := serverstore.ReceiptRow{ContractResult: result,
			ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"` + result + `"}}`}
		if term != "" {
			row.ReceiptJSON = `{"schemaVersion":2,"stages":{"contract":"` + result + `"},` +
				`"stageFailures":{"contract":{"terminationKind":"` + term + `","exitCode":1,` +
				`"evidenceQuality":"complete"}}}`
		}
		return row
	}

	for _, tc := range []struct {
		name      string
		rows      []serverstore.ReceiptRow
		outOfTime bool
	}{
		{"one timeout is a bad night", []serverstore.ReceiptRow{timeout()}, false},
		{"two is a pattern", []serverstore.ReceiptRow{timeout(), timeout()}, true},
		{"nothing has run yet", nil, false},
		// A judgement settles it whichever way it went: the sample WAS
		// measured, so time is not what stands between it and an answer.
		{"a pass among them", []serverstore.ReceiptRow{timeout(), timeout(), judged("PASS", "")}, false},
		{"a real failing assertion among them",
			[]serverstore.ReceiptRow{timeout(), timeout(), judged("FAIL", "exit")}, false},
		// SKIPPED is the verifier reporting it measured nothing either, but
		// it is the RESOLVE stage's problem and has its own retry.
		{"skips do not count against the clock",
			[]serverstore.ReceiptRow{timeout(), judged("SKIPPED", "")}, false},
	} {
		if got := crossWorkIsOutOfTime(tc.rows); got != tc.outOfTime {
			t.Errorf("%s: outOfTime=%v, want %v", tc.name, got, tc.outOfTime)
		}
	}
}

// Lane reconciliation must not manufacture attempts the retry cap forbids.
//
// It reopens an unsupported job the moment a lane serves its coordinates,
// which is right when "unsupported" meant no image existed. Recording a
// timed-out sample the same way puts a row in front of it whose coordinates
// ARE serveable, so every boot would hand it back to the fleet and the cap
// that bounds cross attempts -- which counts job ROWS, and never sees a
// status flipped in place -- would never apply again.
func TestLaneReconcileWillNotReopenPastTheAttemptCap(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	id := "sha256:" + strings.Repeat("8b", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(goManifestFromHost("1.26.0"))),
		Status:       "DRAFT", Quarantined: true,
	}); err != nil {
		t.Fatal(err)
	}
	runnable := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		OS: "linux", Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26",
	}
	// The attempts already spent, plus the row that records the sample as
	// out of time -- exactly the shape the receipt path leaves behind.
	var last int64
	for i := 0; i < maxCrossAttempts; i++ {
		status := "done"
		if i == maxCrossAttempts-1 {
			status = serverstore.JobStatusUnsupported
		}
		jobID, err := store.CreateJob(ctx, serverstore.JobRow{
			SampleID: id, Reason: "cross", Status: status,
			WantEnvJSON: string(domain.MustCanonicalJSON(runnable)),
		})
		if err != nil {
			t.Fatal(err)
		}
		last = jobID
	}

	if _, _, err := ReconcileCrossJobLanes(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.Job(ctx, last)
	if err != nil || !ok {
		t.Fatalf("job %d: ok=%v err=%v", last, ok, err)
	}
	if job.Status != serverstore.JobStatusUnsupported {
		t.Errorf("status = %q, want it left at %q: the sample has spent its %d attempts",
			job.Status, serverstore.JobStatusUnsupported, maxCrossAttempts)
	}
}

// Under the cap it still reopens, which is the whole reason the record is a
// job row and not a verdict on the sample.
//
// One production sample timed out four times and then PASSED on the fifth,
// minutes after the lane it kept dying in got faster. "unsupported" has to
// keep meaning "no lane here runs this today", never "this sample is bad".
func TestLaneReconcileStillReopensATimedOutSampleUnderTheCap(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	id := "sha256:" + strings.Repeat("9c", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(goManifestFromHost("1.26.0"))),
		Status:       "DRAFT", Quarantined: true,
	}); err != nil {
		t.Fatal(err)
	}
	runnable := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		OS: "linux", Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26",
	}
	if _, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "done",
		WantEnvJSON: string(domain.MustCanonicalJSON(runnable)),
	}); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: serverstore.JobStatusUnsupported,
		WantEnvJSON: string(domain.MustCanonicalJSON(runnable)),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := ReconcileCrossJobLanes(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	job, ok, err := store.Job(ctx, jobID)
	if err != nil || !ok {
		t.Fatalf("job %d: ok=%v err=%v", jobID, ok, err)
	}
	if job.Status != "open" {
		t.Errorf("status = %q, want it reopened: two attempts of %d are still unspent",
			job.Status, maxCrossAttempts)
	}
}
