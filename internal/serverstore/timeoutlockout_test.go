package serverstore

import (
	"context"
	"testing"
	"time"
)

// R2C-189-adjacent, measured on production 2026-08-31. A contract killed by
// the sandbox timeout is not a verdict on the sample.
//
// The farm node runs at 79% CPU steal, so a `go test` that finishes in a
// minute on an idle box exceeds the 300-second contract timeout. The kill was
// recorded as contract=FAIL — a verdict — and a verdict permanently excludes
// the peer from that sample. Within an hour the network's only verifier had
// excluded itself from every open cross job and throughput went to zero: 16
// jobs open, 0 visible to the one peer that could run them.
//
// This is the SKIPPED lockout one layer deeper. The contract did run; it just
// never finished, and what it reports is that the verifier ran out of time.
func TestATimedOutContractIsNotAVerdict(t *testing.T) {
	for _, tc := range []struct {
		name        string
		result      string
		termination string
		judged      bool
	}{
		{"a real failing assertion", "FAIL", "exit", true},
		{"a passing contract", "PASS", "", true},
		{"killed by the sandbox timeout", "FAIL", "timeout", false},
		{"the process never started", "FAIL", "process-start-failed", false},
		// A signal is ambiguous on purpose: an OOM kill from the memory cap
		// says nothing about the sample, and a segfault in the code under
		// test says everything. Until the receipt distinguishes them, the
		// conservative reading keeps the exclusion.
		{"killed by a signal", "FAIL", "signal", true},
		{"the contract never ran", "SKIPPED", "", false},
	} {
		if got := ContractWasJudged(tc.result, tc.termination, true); got != tc.judged {
			t.Errorf("%s: judged=%v, want %v", tc.name, got, tc.judged)
		}
	}
}

// The queue offers a peer back a sample its contract only timed out on.
//
// The receipt stays — it is signed evidence and the audit trail is the point —
// and it stops locking the one verifier out of work nobody else can do.
func TestATimedOutPeerGetsTheJobBack(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }

	const peer = "ed25519:onlyverifier"
	seedCrossJob(t, f, "sha256:timedout", now)
	seedCrossJob(t, f, "sha256:reallyfailed", now)

	// One contract killed by the timeout, one that ran and failed.
	seedReceipt(t, f, "sha256:timedout", peer, "FAIL", "timeout", now)
	seedReceipt(t, f, "sha256:reallyfailed", peer, "FAIL", "exit", now)

	jobs, err := f.OpenJobsPage(ctx, "", peer, "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, j := range jobs {
		offered = append(offered, j.SampleID)
	}
	if len(offered) != 1 || offered[0] != "sha256:timedout" {
		t.Errorf("offered %v, want only the sample whose contract timed out", offered)
	}
}

func seedCrossJob(t *testing.T, store *Fake, sampleID string, now time.Time) {
	t.Helper()
	if err := store.SaveSample(t.Context(), SampleRow{
		SampleID: sampleID, ManifestJSON: `{"packages":[],"symbols":[]}`,
		Status: "DRAFT", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedReceipt(t *testing.T, store *Fake, sampleID, peer, result, termination string, now time.Time) {
	t.Helper()
	body := `{"schemaVersion":2,"stages":{"contract":"` + result + `"}`
	if termination != "" {
		body += `,"stageFailures":{"contract":{"terminationKind":"` + termination + `"}}`
	}
	body += `}`
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID + "-" + termination,
		PeerID: peer, EnvHash: "env", ContractResult: result,
		ReceiptJSON: body, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// PostgreSQL applies the same rule the Fake does.
//
// The Fake evaluates ContractWasJudged in Go and PostgreSQL evaluates
// contractJudgedSQL in SQL. That split is exactly how the two came to
// disagree about SKIPPED receipts, and it is the one thing that decides
// whether the network's only verifier is given work.
func TestIntegrationTimedOutLockoutParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }
	const peer = "ed25519:onlyverifier"

	for _, store := range []*Fake{f} {
		seedCrossJob(t, store, "sha256:timedout", now)
		seedCrossJob(t, store, "sha256:nostart", now)
		seedCrossJob(t, store, "sha256:reallyfailed", now)
		seedCrossJob(t, store, "sha256:passed", now)
		seedCrossJob(t, store, "sha256:legacyfail", now)
		seedReceipt(t, store, "sha256:timedout", peer, "FAIL", "timeout", now)
		seedReceipt(t, store, "sha256:nostart", peer, "FAIL", "process-start-failed", now)
		seedReceipt(t, store, "sha256:reallyfailed", peer, "FAIL", "exit", now)
		seedReceipt(t, store, "sha256:passed", peer, "PASS", "", now)
		seedLegacyFailReceipt(t, store, "sha256:legacyfail", peer, now)
	}
	seedCrossJobPG(t, pg, "sha256:timedout", now)
	seedCrossJobPG(t, pg, "sha256:nostart", now)
	seedCrossJobPG(t, pg, "sha256:reallyfailed", now)
	seedCrossJobPG(t, pg, "sha256:passed", now)
	seedCrossJobPG(t, pg, "sha256:legacyfail", now)
	seedReceiptPG(t, pg, "sha256:timedout", peer, "FAIL", "timeout", now)
	seedReceiptPG(t, pg, "sha256:nostart", peer, "FAIL", "process-start-failed", now)
	seedReceiptPG(t, pg, "sha256:reallyfailed", peer, "FAIL", "exit", now)
	seedReceiptPG(t, pg, "sha256:passed", peer, "PASS", "", now)
	seedLegacyFailReceiptPG(t, pg, "sha256:legacyfail", peer, now)

	ctx := context.Background()
	offered := func(s interface {
		OpenJobsPage(context.Context, string, string, string, string, int, int) ([]JobRow, error)
	}) map[string]bool {
		rows, err := s.OpenJobsPage(ctx, "", peer, "cross", "", 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, j := range rows {
			out[j.SampleID] = true
		}
		return out
	}
	got, want := offered(pg), offered(f)
	for _, id := range []string{"sha256:timedout", "sha256:nostart", "sha256:reallyfailed", "sha256:passed", "sha256:legacyfail"} {
		if got[id] != want[id] {
			t.Errorf("%s: pg offered=%v fake offered=%v", id, got[id], want[id])
		}
	}
	// And the answer itself: a timeout and a failed start come back, a real
	// verdict does not.
	if !got["sha256:timedout"] || !got["sha256:nostart"] {
		t.Errorf("an infrastructure kill still locks the peer out: %v", got)
	}
	if !got["sha256:legacyfail"] {
		t.Errorf("a FAIL with no failure evidence still locks the peer out: %v", got)
	}
	if got["sha256:reallyfailed"] || got["sha256:passed"] {
		t.Errorf("a real verdict stopped excluding its peer: %v", got)
	}
}

func seedCrossJobPG(t *testing.T, store *PG, sampleID string, now time.Time) {
	t.Helper()
	if err := store.SaveSample(t.Context(), SampleRow{
		SampleID: sampleID, ManifestJSON: `{"packages":[],"symbols":[]}`,
		Status: "DRAFT", License: "MIT-0", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedReceiptPG(t *testing.T, store *PG, sampleID, peer, result, termination string, now time.Time) {
	t.Helper()
	body := `{"schemaVersion":2,"stages":{"contract":"` + result + `"}`
	if termination != "" {
		body += `,"stageFailures":{"contract":{"terminationKind":"` + termination + `"}}`
	}
	body += `}`
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID + "-" + termination,
		PeerID: peer, EnvHash: "env", ContractResult: result,
		ReceiptJSON: body, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// A FAIL that carries no failure evidence at all is not a verdict either.
//
// Measured on production 2026-08-31, split perfectly by date: every FAIL
// receipt written 2026-08-18 to 08-21 carries no stageFailures (63 of them),
// and every FAIL receipt written 08-28 onward carries them (29 of 29). The
// stage-failure contract landed in between, so evidence-less FAILs are a
// closed historical set that cannot grow.
//
// Eleven of those sat on the network's only verifier as permanent exclusions
// from samples nobody else can check, all from the window the PrivateTmp
// incident covers. They claim a verdict and hold nothing that would let
// anyone check it — which is exactly what EvidenceLegacyIncomplete exists to
// say: a historical record must not masquerade as complete modern evidence.
//
// A PASS needs no such evidence. There is nothing to evidence about a
// contract that ran and passed.
func TestAFailWithNoFailureEvidenceIsNotAVerdict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		result   string
		evidence bool
		judged   bool
	}{
		{"a modern FAIL with its failure recorded", "FAIL", true, true},
		{"a 2026-08-20 FAIL with nothing recorded", "FAIL", false, false},
		{"a PASS, which has nothing to evidence", "PASS", false, true},
	} {
		if got := ContractWasJudged(tc.result, "", tc.evidence); got != tc.judged {
			t.Errorf("%s: judged=%v, want %v", tc.name, got, tc.judged)
		}
	}
}

// seedLegacyFailReceipt writes the shape every FAIL receipt had before the
// stage-failure contract: a verdict with nothing recorded about the failure.
func seedLegacyFailReceipt(t *testing.T, store *Fake, sampleID, peer string, now time.Time) {
	t.Helper()
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID + "-legacy",
		PeerID: peer, EnvHash: "env", ContractResult: "FAIL",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"FAIL"}}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedLegacyFailReceiptPG(t *testing.T, store *PG, sampleID, peer string, now time.Time) {
	t.Helper()
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID + "-legacy",
		PeerID: peer, EnvHash: "env", ContractResult: "FAIL",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"FAIL"}}`, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}
