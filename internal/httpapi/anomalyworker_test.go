package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/verifier"
)

// This is the assertion the whole design of the ingest path was decided by,
// and it is checked with the REAL worker rather than a hand-written HTTP call.
//
// `verifier.CrossVerifier.FetchJob` is the code running on every contributor
// machine and every farm node today. It lists the queue, skips any reason it
// does not recognize, checks the requirements against the images this build
// pins, and claims. A report that queued work that code will not take would
// sit "queued" forever while the dashboard showed a healthy queue — and that
// exact failure has already happened once to this project, for three days, in
// a different disguise.
//
// So: file a report, then let the real worker go looking for work.
func TestTheRealVerifierWorkerClaimsWorkQueuedByAnAnomalyReport(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := context.Background()

	// A sample the fleet can actually run: the requirements a cross job
	// builds come from this manifest, and a worker that has no image for
	// them is right to skip it.
	manifest := testManifest()
	manifest.Environment = domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.1", ModuleSystem: "esm",
	}
	artifact := buildArtifact(t, manifest, map[string]string{
		"src/echo.mjs":      "export function echo(x){ return x }\n",
		"test/contract.mjs": "console.log('ok')\n",
	})
	sampleID := domain.SHA256Hex(artifact)
	if resp := postSample(t, srv.URL, manifest, sampleID, artifact, ""); resp.StatusCode != http.StatusCreated {
		t.Fatalf("sample upload status = %d", resp.StatusCode)
	}
	// Publication queues its own cross job. Close it, so the only claimable
	// work left is whatever the report creates — otherwise this test could
	// pass on the wrong job.
	jobs, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if err := store.CompleteJob(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
	}

	report := mismatchAgainst(sampleID)
	report.Environment = manifest.Environment
	var out anomalyResponse
	if resp := postJSON(t, srv.URL+"/v1/anomalies", anomalyEnvelopeFor(report), &out); resp.StatusCode != http.StatusOK {
		t.Fatalf("report status = %d", resp.StatusCode)
	}
	if out.VerificationJobID == 0 {
		t.Fatalf("the report queued nothing: %+v", out)
	}

	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worker := &verifier.CrossVerifier{
		HTTP:        srv.Client(),
		ServerURL:   srv.URL,
		Ident:       ident,
		Cap:         domain.CapContainerRun,
		ContainerOS: "linux",
		Env:         manifest.Environment,
	}
	job, err := worker.FetchJob(ctx)
	if err != nil {
		t.Fatalf("the deployed worker could not fetch work: %v", err)
	}
	if job == nil {
		t.Fatalf("the deployed worker found nothing to claim; unsupported coordinates it skipped: %v",
			worker.UnsupportedWork())
	}
	if job.ID != out.VerificationJobID {
		t.Fatalf("the worker claimed job %d, the report queued %d", job.ID, out.VerificationJobID)
	}

	// And the claim moved the report, so an operator can tell a slow fleet
	// from a stuck one.
	claimed, _, err := store.AnomalyReportByID(ctx, out.ReportID)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Status != domain.AnomalyStatusVerifying {
		t.Fatalf("report status after a real claim = %q", claimed.Status)
	}
	stored, found, err := store.Job(ctx, job.ID)
	if err != nil || !found || stored.Status != "claimed" || stored.ClaimedBy != ident.PeerID() {
		t.Fatalf("queue row after the claim = %+v found=%v err=%v", stored, found, err)
	}
}
