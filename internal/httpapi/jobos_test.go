package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A cross job asks another machine to reproduce the sample in the
// environment the sample needs, and the OS is part of that. The queue
// filters on want_env->>'os' and the worker reports its container OS — but
// nothing ever WROTE the key, so the filter matched every job: a Linux
// verifier kept claiming Windows-only work, filed SKIPPED receipts, and
// burned the sample's bounded cross attempts on machines that could never
// run it.
func TestCrossJobCarriesTheSampleOS(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	manifest := testManifest()
	// A farm-authored draft records the container it ran in; that OS is the
	// platform the sample answers for.
	manifest.Environment.OS = "windows"
	manifest.Environment.Virtualization = "container"
	id := "sha256:" + strings.Repeat("ab", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "SELF_PASS",
	}); err != nil {
		t.Fatal(err)
	}
	if err := queueCrossVerificationOn(ctx, store, id); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.JobsForSample(ctx, id)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err=%v, want exactly one cross job", jobs, err)
	}
	want, err := decodeWorkerRequirements(jobs[0].WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.OS != "windows" {
		t.Fatalf("job os = %q, want the sample's %q", want.OS, "windows")
	}
}

// A manifest that records the AUTHOR'S HOST pins nothing. A user proposing
// an npm sample from a Windows laptop records os=windows on an environment
// that is not where verification runs — no npm verifier serves Windows, and
// pinning that OS would strand the sample forever. Only an environment that
// IS an execution environment (a container run) may pin.
func TestCrossJobWithoutASampleOSPinsNothing(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	manifest := testManifest() // host fingerprint: os windows, no virtualization
	id := "sha256:" + strings.Repeat("cd", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "SELF_PASS",
	}); err != nil {
		t.Fatal(err)
	}
	if err := queueCrossVerificationOn(ctx, store, id); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.JobsForSample(ctx, id)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %+v err=%v", jobs, err)
	}
	want, err := decodeWorkerRequirements(jobs[0].WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.OS != "" {
		t.Fatalf("job os = %q, want unpinned", want.OS)
	}
}

// The receipt closes the loop: a job that pins an OS is answered only by a
// receipt that ran there. Without this, a claim that slipped past the queue
// filter would still be accepted and stamp the wrong platform.
func TestReceiptOSMustMatchTheJobOS(t *testing.T) {
	receipt := domain.VerificationReceipt{
		SandboxCapability: domain.CapContainerRun,
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
			Runtime: "node", RuntimeVersion: "22.18.1",
		},
	}
	pinned := domain.WorkerRequirements{OS: "windows"}
	if receiptMatchesRequirements(receipt, pinned) {
		t.Error("a linux receipt satisfied a windows-pinned job")
	}
	if !receiptMatchesRequirements(receipt, domain.WorkerRequirements{OS: "linux"}) {
		t.Error("a linux receipt was refused by a linux-pinned job")
	}
	if !receiptMatchesRequirements(receipt, domain.WorkerRequirements{}) {
		t.Error("an unpinned job refused a receipt")
	}
}
