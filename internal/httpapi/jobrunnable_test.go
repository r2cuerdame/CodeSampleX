package httpapi

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// goManifestFromHost is the shape production actually produced: a Go sample
// proposed from a developer's own machine, whose environment records that
// machine's toolchain rather than a container the sample ran in.
func goManifestFromHost(runtimeVersion string) domain.SampleManifest {
	m := testManifest()
	m.Case.Goal = "verify pkg:golang/github.com/mattn/go-isatty@v0.0.20"
	m.Case.Packages = []string{"pkg:golang/github.com/mattn/go-isatty@v0.0.20"}
	m.Packages = m.Case.Packages
	m.Symbols = []string{"isatty.IsTerminal"}
	m.ContractCommand = []string{"go", "test", "./..."}
	m.VerifierAdapter = "golang@1"
	m.Environment = domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Arch: "x64",
		Runtime: "go", RuntimeVersion: runtimeVersion, Language: "go",
		PackageManager: "go", PackageManagerVersion: runtimeVersion,
		Virtualization: "vm", OSVersionBucket: "10",
	}
	return m
}

func queueOneCrossJob(t *testing.T, store *serverstore.Fake, id string, m domain.SampleManifest) serverstore.JobRow {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(m)), Status: "SELF_PASS",
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
	return jobs[0]
}

// The author's toolchain is not a verifier lane.
//
// Production stood still on this. A Windows machine whose Go had moved to
// 1.27.0 proposed three Go samples; every cross job copied "1.27" out of that
// host fingerprint, and the only Go lane this binary has is golang:1.26-alpine.
// runtimeVersionMatches("1.26","1.27") is false, so every worker skipped the
// rows in canPrepare BEFORE claiming and `csx worker start --once` reported
// completed=0 failed=0 forever. The jobs were open, offered, and impossible.
//
// A cross job may only ask for precision the fleet can actually serve. The
// receipt records the runtime the container really ran, which is the whole
// point of asking a different machine.
func TestACrossJobNeverAsksForARuntimeLineNoLaneServes(t *testing.T) {
	store := serverstore.NewFake()
	job := queueOneCrossJob(t, store, "sha256:"+strings.Repeat("1a", 32), goManifestFromHost("1.27.0"))

	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.RuntimeVersion != "" {
		t.Errorf("job runtime version = %q, want unpinned: no verifier lane serves it", want.RuntimeVersion)
	}
	if !sandbox.ContainerSupportsRequirements(want) {
		t.Fatalf("no container worker can claim the job this sample produced: %+v", want)
	}
	if job.Status != "open" {
		t.Errorf("job status = %q, want open: the work is runnable", job.Status)
	}
	// The dimensions that say WHICH sample this is are never relaxed.
	if want.Ecosystem != "golang" || want.Runtime != "go" || want.VerifierAdapter != "golang@1" {
		t.Errorf("relaxing the version lost the sample's own coordinates: %+v", want)
	}
}

// A line the fleet does serve keeps its pin. Relaxation is a repair for an
// impossible requirement, not a general loss of precision.
func TestACrossJobKeepsARuntimeLineALaneServes(t *testing.T) {
	store := serverstore.NewFake()
	job := queueOneCrossJob(t, store, "sha256:"+strings.Repeat("2b", 32), goManifestFromHost("1.26.5"))

	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.RuntimeVersion != "1.26" {
		t.Errorf("job runtime version = %q, want the served line %q", want.RuntimeVersion, "1.26")
	}
	if !sandbox.ContainerSupportsRequirements(want) {
		t.Fatalf("no container worker can claim the job this sample produced: %+v", want)
	}
}

// npm is the same rule seen from the other side: node 22 is a lane, so the
// pin survives, and a browser sample keeps every browser dimension.
func TestACrossJobKeepsServedNodeAndBrowserPins(t *testing.T) {
	store := serverstore.NewFake()
	m := testManifest()
	m.Environment.ExecutionContext = "node"
	job := queueOneCrossJob(t, store, "sha256:"+strings.Repeat("3c", 32), m)
	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.RuntimeVersion == "" || want.ExecutionContext != "node" {
		t.Errorf("a served node lane lost its precision: %+v", want)
	}
	if !sandbox.ContainerSupportsRequirements(want) {
		t.Fatalf("no container worker can claim the job this sample produced: %+v", want)
	}
}

// A value no lane serves is relaxed too, when relaxing leaves something
// runnable. Job 6038 in production named executionContext "host" — a value
// the author's machine supplied and no verifier image provides. Dropping it
// leaves an ordinary Go job the fleet can run, and the receipt records the
// context the container really had.
func TestAnUnservedExecutionContextIsRelaxedRatherThanStranded(t *testing.T) {
	store := serverstore.NewFake()
	m := goManifestFromHost("1.27.0")
	m.Environment.ExecutionContext = "host"
	job := queueOneCrossJob(t, store, "sha256:"+strings.Repeat("4d", 32), m)

	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.ExecutionContext != "" || want.RuntimeVersion != "" {
		t.Errorf("requirements no lane serves survived: %+v", want)
	}
	if job.Status != "open" || !sandbox.ContainerSupportsRequirements(want) {
		t.Fatalf("status=%q requirements=%+v, want claimable open work", job.Status, want)
	}
}

// Some coordinates have no lane at all, and relaxing does not invent one. A
// Firefox contract has no pinned Firefox image in this build. Such work must
// not sit in the open queue pretending to be claimable — an operator can
// count "unsupported", and cannot count silence — and it must never be
// demoted onto a lane that would answer a different question: dropping the
// browser context to run it on plain Node would produce a confident receipt
// about an environment nobody asked about.
func TestWorkNoLaneCanRunIsRecordedUnsupportedRatherThanLeftOpen(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	m := testManifest()
	m.Environment.ExecutionContext = "browser"
	m.Environment.BrowserFamily = "firefox"
	m.Environment.BrowserMajor = "141"
	m.Environment.Engine = "gecko"
	job := queueOneCrossJob(t, store, "sha256:"+strings.Repeat("5e", 32), m)

	if job.Status != serverstore.JobStatusUnsupported {
		t.Fatalf("job status = %q, want %q", job.Status, serverstore.JobStatusUnsupported)
	}
	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if want.BrowserFamily != "firefox" || want.ExecutionContext != "browser" {
		t.Errorf("unsupported work was rewritten instead of recorded: %+v", want)
	}
	open, err := store.OpenJobs(ctx, string(domain.CapContainerRun), "ed25519:peer", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range open {
		if j.ID == job.ID {
			t.Fatal("work no lane can run was still offered to a worker")
		}
	}
}

// The fix above only reaches jobs created after it ships. The three jobs that
// stopped production were created days earlier and nothing in the request
// path ever looks at an open job again — the same absence ReconcileStranded-
// Drafts exists for. Boot re-derives what a cross job may ask for and either
// repairs it or records that no lane can run it.
func TestBootRepairsCrossJobsAskingForALaneNoneServes(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	id := "sha256:" + strings.Repeat("6f", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(goManifestFromHost("1.27.0"))),
		Status:       "DRAFT",
	}); err != nil {
		t.Fatal(err)
	}
	// Exactly what production held: the pre-fix requirements, still open.
	stale := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.27",
	}
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "open",
		WantEnvJSON: string(domain.MustCanonicalJSON(stale)),
	})
	if err != nil {
		t.Fatal(err)
	}

	repaired, unsupported, err := ReconcileCrossJobLanes(ctx, store, 100)
	if err != nil {
		t.Fatal(err)
	}
	if repaired != 1 || unsupported != 0 {
		t.Fatalf("reconcile repaired=%d unsupported=%d, want 1 and 0", repaired, unsupported)
	}
	job, ok, err := store.Job(ctx, jobID)
	if err != nil || !ok {
		t.Fatalf("job %d: ok=%v err=%v", jobID, ok, err)
	}
	want, err := decodeWorkerRequirements(job.WantEnvJSON)
	if err != nil {
		t.Fatalf("job requirements do not parse: %v", err)
	}
	if job.Status != "open" || !sandbox.ContainerSupportsRequirements(want) {
		t.Fatalf("status=%q requirements=%+v, want claimable open work", job.Status, want)
	}
	// Running it again changes nothing: the repair is idempotent.
	repaired, unsupported, err = ReconcileCrossJobLanes(ctx, store, 100)
	if err != nil || repaired != 0 || unsupported != 0 {
		t.Fatalf("second pass repaired=%d unsupported=%d err=%v, want a no-op", repaired, unsupported, err)
	}
}

// A stale claim is a job the queue already offers again, so the repair must
// reach it too — both jobs that stopped production were held by a peer that
// walked away in August and never filed a receipt.
func TestBootRepairsAJobHeldByAClaimThatOutlivedItsLease(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	id := "sha256:" + strings.Repeat("7a", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(goManifestFromHost("1.27.0"))),
		Status:       "DRAFT",
	}); err != nil {
		t.Fatal(err)
	}
	stale := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.27",
	}
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: "claimed",
		ClaimedBy: "ed25519:2a6aa94bf40f1df0", ClaimedAt: time.Now().Add(-72 * time.Hour),
		WantEnvJSON: string(domain.MustCanonicalJSON(stale)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReconcileCrossJobLanes(ctx, store, 100); err != nil {
		t.Fatal(err)
	}
	job, _, err := store.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "open" || job.ClaimedBy != "" {
		t.Fatalf("job = %+v, want an unclaimed open job", job)
	}
}

// Unsupported is a statement about this build's images, not about the sample.
// When a lane appears the work must come back on its own, or an operator has
// to remember it — which is the failure mode this whole issue is.
func TestBootReopensUnsupportedWorkOnceALaneServesIt(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()

	id := "sha256:" + strings.Repeat("8b", 32)
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     id,
		ManifestJSON: string(domain.MustCanonicalJSON(goManifestFromHost("1.26.5"))),
		Status:       "DRAFT",
	}); err != nil {
		t.Fatal(err)
	}
	served := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun, VerifierAdapter: "golang@1",
		Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26",
	}
	jobID, err := store.CreateJob(ctx, serverstore.JobRow{
		SampleID: id, Reason: "cross", Status: serverstore.JobStatusUnsupported,
		WantEnvJSON: string(domain.MustCanonicalJSON(served)),
	})
	if err != nil {
		t.Fatal(err)
	}
	repaired, _, err := ReconcileCrossJobLanes(ctx, store, 100)
	if err != nil || repaired != 1 {
		t.Fatalf("repaired=%d err=%v, want the job reopened", repaired, err)
	}
	job, _, err := store.Job(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "open" {
		t.Fatalf("job status = %q, want open", job.Status)
	}
}
