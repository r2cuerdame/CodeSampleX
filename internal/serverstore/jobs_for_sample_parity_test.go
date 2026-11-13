package serverstore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// runJobsForSampleOrderingContract is written once and played against both
// stores. seed makes the sample exist (PostgreSQL enforces the foreign key);
// stamp pins a job's created_at where the store does not take it from the
// JobRow (PostgreSQL's CreateJob lets the column default decide).
func runJobsForSampleOrderingContract(t *testing.T, store Store, seed func(sampleID string), stamp func(id int64, at time.Time)) {
	t.Helper()
	ctx := context.Background()
	sampleID := "sha256:" + fmt.Sprintf("%064d", 627)
	seed(sampleID)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// Insert newest first. The two oldest jobs share a timestamp so their IDs
	// must determine their order independently of their insertion position.
	want := []JobRow{
		{SampleID: sampleID, Reason: "cross", Status: "open", CreatedAt: base},
		{SampleID: sampleID, Reason: "cross", Status: "done", CreatedAt: base},
		{SampleID: sampleID, Reason: "cross", Status: "claimed", CreatedAt: base.Add(time.Hour)},
	}
	for i := len(want) - 1; i >= 0; i-- {
		id, err := store.CreateJob(ctx, want[i])
		if err != nil {
			t.Fatalf("CreateJob[%d]: %v", i, err)
		}
		want[i].ID = id
		if stamp != nil {
			stamp(id, want[i].CreatedAt)
		}
	}

	got, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		t.Fatalf("JobsForSample: %v", err)
	}
	// Oldest first; the equal-time pair falls back to ID ascending. IDs follow
	// insertion order, and want[1] was inserted before want[0].
	expected := []JobRow{want[1], want[0], want[2]}
	if want[1].ID > want[0].ID {
		expected[0], expected[1] = expected[1], expected[0]
	}
	describe := func(rows []JobRow) []string {
		out := make([]string, 0, len(rows))
		for _, j := range rows {
			// Compare instants, not time.Time values: PostgreSQL hands
			// timestamptz back in the local zone.
			out = append(out, fmt.Sprintf("id=%d reason=%s status=%s created=%s",
				j.ID, j.Reason, j.Status, j.CreatedAt.UTC().Format(time.RFC3339Nano)))
		}
		return out
	}
	gotLines, wantLines := describe(got), describe(expected)
	if fmt.Sprint(gotLines) != fmt.Sprint(wantLines) {
		t.Fatalf("JobsForSample order =\n%q\nwant\n%q", gotLines, wantLines)
	}
}

func TestJobsForSampleOrderingParity(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	runJobsForSampleOrderingContract(t, fake, func(sampleID string) {
		if err := fake.SaveSample(ctx, SampleRow{
			SampleID: sampleID, ManifestJSON: `{"schemaVersion":1}`,
		}); err != nil {
			t.Fatal(err)
		}
	}, nil)
}

func TestIntegrationPGJobsForSampleOrderingParity(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	caseID := "case:sha256:jobs-for-sample-order"
	runJobsForSampleOrderingContract(t, pg, func(sampleID string) {
		if err := pg.SaveCase(ctx, domain.Case{
			SchemaVersion: 1, CaseID: caseID, Kind: "HOW", Goal: "jobs for sample order",
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
	}, func(id int64, at time.Time) {
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `UPDATE verification_jobs SET created_at=$2 WHERE id=$1`, id, at)
			return err
		}); err != nil {
			t.Fatalf("stamp job %d created_at: %v", id, err)
		}
	})
}
