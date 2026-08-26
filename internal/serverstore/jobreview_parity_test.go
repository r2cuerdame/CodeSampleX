package serverstore

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// jobLaneScript plays one sequence through a store and returns what an
// operator would read back. Written once and run against both stores: the
// Fake and PG have drifted apart before, and a Fake that disagrees with
// production lets a test prove a repair the server would never make.
func jobLaneScript(t *testing.T, ctx context.Context, store Store, seed func(sampleID string)) []string {
	t.Helper()
	sampleID := "sha256:" + fmt.Sprintf("%064d", 94)
	seed(sampleID)

	unrunnable := `{"ecosystem":"golang","runtime":"go","runtimeVersion":"1.27","sandboxCapability":"CONTAINER_RUN","verifierAdapter":"golang@1"}`
	openID, err := store.CreateJob(ctx, JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open", WantEnvJSON: unrunnable,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Work created as unsupported must never enter the open queue, and the
	// column default would have made it open.
	unsupportedID, err := store.CreateJob(ctx, JobRow{
		SampleID: sampleID, Reason: "cross", Status: JobStatusUnsupported, WantEnvJSON: unrunnable,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A claim nobody came back to. Both jobs that stopped production were
	// held this way since August and never answered.
	staleID, err := store.CreateJob(ctx, JobRow{
		SampleID: sampleID, Reason: "cross", Status: "claimed",
		ClaimedBy: "ed25519:2a6aa94bf40f1df0", ClaimedAt: time.Now().Add(-72 * time.Hour),
		WantEnvJSON: unrunnable,
	})
	if err != nil {
		t.Fatal(err)
	}

	var out []string
	offered, err := store.OpenJobs(ctx, string(domain.CapContainerRun), "ed25519:someone", "cross", 20)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, fmt.Sprintf("offered-unsupported=%v", jobRowsContain(offered, unsupportedID)))

	review, err := store.CrossJobsForLaneReview(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[int64]string{}
	for _, j := range review {
		seen[j.ID] = j.Status
	}
	out = append(out, fmt.Sprintf("review-open=%q review-unsupported=%q review-stale=%q",
		seen[openID], seen[unsupportedID], seen[staleID]))

	repaired := `{"ecosystem":"golang","runtime":"go","sandboxCapability":"CONTAINER_RUN","verifierAdapter":"golang@1"}`
	for _, id := range []int64{openID, unsupportedID, staleID} {
		if err := store.SetJobRequirements(ctx, id, repaired, "open"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{openID, unsupportedID, staleID} {
		job, ok, err := store.Job(ctx, id)
		if err != nil || !ok {
			t.Fatalf("job %d: ok=%v err=%v", id, ok, err)
		}
		// PostgreSQL returns jsonb with its own key order; compare the
		// requirements the two stores actually hold, not their spelling.
		var held domain.WorkerRequirements
		if err := json.Unmarshal([]byte(job.WantEnvJSON), &held); err != nil {
			t.Fatalf("job %d requirements do not parse: %v", id, err)
		}
		out = append(out, fmt.Sprintf("status=%q claimedBy=%q wantEnv=%s",
			job.Status, job.ClaimedBy, domain.MustCanonicalJSON(held)))
	}
	return out
}

func TestIntegrationCrossJobLaneReviewFakeMatchesPostgres(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	caseID := "case:sha256:lane-review"
	fromPG := jobLaneScript(t, ctx, pg, func(sampleID string) {
		if err := pg.SaveCase(ctx, domain.Case{
			SchemaVersion: 1, CaseID: caseID, Kind: "HOW", Goal: "lane review",
			Packages: []string{"pkg:golang/github.com/mattn/go-isatty@v0.0.20"},
			Contract: []string{"passes"},
		}); err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveSample(ctx, SampleRow{
			SampleID: sampleID, CaseID: caseID, ManifestJSON: `{"schemaVersion":1}`,
		}); err != nil {
			t.Fatal(err)
		}
	})

	fake := NewFake()
	fromFake := jobLaneScript(t, ctx, fake, func(sampleID string) {
		if err := fake.SaveSample(ctx, SampleRow{
			SampleID: sampleID, ManifestJSON: `{"schemaVersion":1}`,
		}); err != nil {
			t.Fatal(err)
		}
	})

	if len(fromPG) != len(fromFake) {
		t.Fatalf("pg=%v fake=%v", fromPG, fromFake)
	}
	for i := range fromPG {
		if fromPG[i] != fromFake[i] {
			t.Errorf("line %d: pg=%q fake=%q", i, fromPG[i], fromFake[i])
		}
	}
	// And the sequence is the one the repair depends on, not merely equal.
	want := []string{
		"offered-unsupported=false",
		`review-open="open" review-unsupported="unsupported" review-stale="claimed"`,
		`status="open" claimedBy="" wantEnv={"ecosystem":"golang","runtime":"go","sandboxCapability":"CONTAINER_RUN","verifierAdapter":"golang@1"}`,
		`status="open" claimedBy="" wantEnv={"ecosystem":"golang","runtime":"go","sandboxCapability":"CONTAINER_RUN","verifierAdapter":"golang@1"}`,
		`status="open" claimedBy="" wantEnv={"ecosystem":"golang","runtime":"go","sandboxCapability":"CONTAINER_RUN","verifierAdapter":"golang@1"}`,
	}
	for i := range want {
		if fromPG[i] != want[i] {
			t.Errorf("line %d: got %q, want %q", i, fromPG[i], want[i])
		}
	}
}
