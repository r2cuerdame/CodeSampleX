package serverstore

import (
	"context"
	"testing"
	"time"
)

// A receipt produced in an environment the sample never claimed is not a
// verdict on the sample.
//
// Measured on production 2026-09-01, after v0.1.81 fixed the image selector.
// All eight open cross jobs were invisible to the network's only verifier,
// every one of them excluded by a FAIL receipt that records:
//
//	sample declared   linux/x64/glibc
//	receipt ran in    linux/x64/musl
//
// Seven died with ERR_DLOPEN_FAILED — glibc's dynamic linker is not in an
// Alpine image — and the eighth exited 1 for the same reason. The contract
// ran, so it is not the SKIPPED case; it did not time out, so it is not that
// case either; it recorded its failure, so it is not the evidence-less case.
// It simply ran somewhere the sample never said it would work.
//
// The discriminator is structural, not textual. Matching on
// "ERR_DLOPEN_FAILED" would be a guess that the next environment bug spells
// itself the same way, and would also silence a sample that genuinely fails
// with those letters in it. The receipt states the environment it ran in and
// the manifest states the one it asked for; when they disagree, the receipt
// is evidence about the verifier.
func TestAReceiptFromTheWrongEnvironmentIsNotAVerdict(t *testing.T) {
	for _, tc := range []struct {
		name            string
		declared, ranIn string
		judged          bool
	}{
		{"the environment the sample asked for", "glibc", "glibc", true},
		{"musl where the sample declared glibc", "glibc", "musl", false},
		{"glibc where the sample declared musl", "musl", "glibc", false},
		{"the sample declared nothing, so nothing is contradicted", "", "musl", true},
		{"the receipt does not say where it ran", "glibc", "", true},
	} {
		got := EnvironmentAgrees(
			map[string]string{"libc": tc.declared},
			map[string]string{"libc": tc.ranIn},
		)
		if got != tc.judged {
			t.Errorf("%s: agrees=%v, want %v", tc.name, got, tc.judged)
		}
	}
}

// Only the dimensions that decide whether the code can run at all.
//
// os, arch and libc are the ones that make a binary load or not. A different
// distro or OS version bucket is exactly the kind of difference this network
// exists to observe, and treating it as a wrong environment would discard
// real verdicts.
func TestOnlyLoadBearingDimensionsCountAsAMismatch(t *testing.T) {
	declared := map[string]string{
		"os": "linux", "arch": "x64", "libc": "glibc",
		"osVersionBucket": "bookworm", "runtimeVersion": "22",
	}
	same := map[string]string{
		"os": "linux", "arch": "x64", "libc": "glibc",
		"osVersionBucket": "trixie", "runtimeVersion": "24",
	}
	if !EnvironmentAgrees(declared, same) {
		t.Error("a different distro bucket was read as the wrong machine")
	}
	for _, dim := range []string{"os", "arch", "libc"} {
		wrong := map[string]string{"os": "linux", "arch": "x64", "libc": "glibc"}
		wrong[dim] = "something-else"
		if EnvironmentAgrees(declared, wrong) {
			t.Errorf("a receipt from a different %s counted as a verdict", dim)
		}
	}
}

// The queue offers back a sample whose only receipt came from the wrong
// machine, and keeps hiding one whose receipt came from the right one.
func TestTheQueueReturnsJobsJudgedInTheWrongEnvironment(t *testing.T) {
	f := NewFake()
	ctx := context.Background()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }
	const peer = "ed25519:onlyverifier"

	seedEnvSample(t, f, "sha256:wrongenv", "glibc", now)
	seedEnvSample(t, f, "sha256:rightenv", "glibc", now)
	seedEnvReceipt(t, f, "sha256:wrongenv", peer, "musl", now)
	seedEnvReceipt(t, f, "sha256:rightenv", peer, "glibc", now)

	jobs, err := f.OpenJobsPage(ctx, "", peer, "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offered []string
	for _, j := range jobs {
		offered = append(offered, j.SampleID)
	}
	if len(offered) != 1 || offered[0] != "sha256:wrongenv" {
		t.Errorf("offered %v, want only the sample judged on the wrong machine", offered)
	}
}

func seedEnvSample(t *testing.T, store *Fake, sampleID, libc string, now time.Time) {
	t.Helper()
	env := `"os":"linux","arch":"x64"`
	if libc != "" {
		env += `,"libc":"` + libc + `"`
	}
	if err := store.SaveSample(t.Context(), SampleRow{
		SampleID: sampleID, Status: "DRAFT", License: "MIT-0", CreatedAt: now,
		ManifestJSON: `{"packages":[],"symbols":[],"environment":{"schemaVersion":1,` + env + `}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedEnvReceipt(t *testing.T, store *Fake, sampleID, peer, libc string, now time.Time) {
	t.Helper()
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID, PeerID: peer,
		EnvHash: "env-" + libc, ContractResult: "FAIL", CreatedAt: now,
		ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"FAIL"},` +
			`"environment":{"os":"linux","arch":"x64","libc":"` + libc + `"},` +
			`"stageFailures":{"contract":{"terminationKind":"exit","evidenceQuality":"complete"}}}`,
	}); err != nil {
		t.Fatal(err)
	}
}

// PostgreSQL applies the same rule.
//
// The Fake reads the two environments in Go and PostgreSQL compares them in
// SQL. Every rule that has ever governed this exclusion drifted between those
// two halves at least once, which is why each one gets a parity test.
func TestIntegrationWrongEnvironmentParity(t *testing.T) {
	pg := openTestPG(t)
	f := NewFake()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	f.NowFn = func() time.Time { return now }
	const peer = "ed25519:onlyverifier"

	seedEnvSample(t, f, "sha256:wrongenv", "glibc", now)
	seedEnvSample(t, f, "sha256:rightenv", "glibc", now)
	seedEnvSample(t, f, "sha256:undeclared", "", now)
	seedEnvReceipt(t, f, "sha256:wrongenv", peer, "musl", now)
	seedEnvReceipt(t, f, "sha256:rightenv", peer, "glibc", now)
	seedEnvReceipt(t, f, "sha256:undeclared", peer, "musl", now)

	seedEnvSamplePG(t, pg, "sha256:wrongenv", "glibc", now)
	seedEnvSamplePG(t, pg, "sha256:rightenv", "glibc", now)
	seedEnvSamplePG(t, pg, "sha256:undeclared", "", now)
	seedEnvReceiptPG(t, pg, "sha256:wrongenv", peer, "musl", now)
	seedEnvReceiptPG(t, pg, "sha256:rightenv", peer, "glibc", now)
	seedEnvReceiptPG(t, pg, "sha256:undeclared", peer, "musl", now)

	ctx := context.Background()
	read := func(rows []JobRow, err error) map[string]bool {
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, j := range rows {
			out[j.SampleID] = true
		}
		return out
	}
	got := read(pg.OpenJobsPage(ctx, "", peer, "cross", "", 50, 0))
	want := read(f.OpenJobsPage(ctx, "", peer, "cross", "", 50, 0))
	for _, id := range []string{"sha256:wrongenv", "sha256:rightenv", "sha256:undeclared"} {
		if got[id] != want[id] {
			t.Errorf("%s: pg offered=%v fake offered=%v", id, got[id], want[id])
		}
	}
	if !got["sha256:wrongenv"] {
		t.Error("a receipt from the wrong machine still locks the peer out")
	}
	if got["sha256:rightenv"] {
		t.Error("a verdict from the right machine stopped excluding its peer")
	}
	// A sample that declared no libc made no claim about one, so a musl
	// receipt contradicts nothing and remains a verdict.
	if got["sha256:undeclared"] {
		t.Error("a sample that declared nothing had its verdict discarded")
	}
}

func seedEnvSamplePG(t *testing.T, store *PG, sampleID, libc string, now time.Time) {
	t.Helper()
	env := `"os":"linux","arch":"x64"`
	if libc != "" {
		env += `,"libc":"` + libc + `"`
	}
	if err := store.SaveSample(t.Context(), SampleRow{
		SampleID: sampleID, Status: "DRAFT", License: "MIT-0", CreatedAt: now,
		ManifestJSON: `{"packages":[],"symbols":[],"environment":{"schemaVersion":1,` + env + `}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateJob(t.Context(), JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedEnvReceiptPG(t *testing.T, store *PG, sampleID, peer, libc string, now time.Time) {
	t.Helper()
	if err := store.SaveReceipt(t.Context(), ReceiptRow{
		SampleID: sampleID, ReceiptID: "r-" + sampleID, PeerID: peer,
		EnvHash: "env-" + libc, ContractResult: "FAIL", CreatedAt: now,
		ReceiptJSON: `{"schemaVersion":2,"stages":{"contract":"FAIL"},` +
			`"environment":{"os":"linux","arch":"x64","libc":"` + libc + `"},` +
			`"stageFailures":{"contract":{"terminationKind":"exit","evidenceQuality":"complete"}}}`,
	}); err != nil {
		t.Fatal(err)
	}
}
