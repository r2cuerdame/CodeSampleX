package serverstore

import (
	"context"
	"fmt"
	"testing"
)

// The completeness scheduler decides what the farm does next, and it decides
// it twice -- once in Go for the tests and once in SQL for production. A Fake
// that answers differently from PostgreSQL lets a test prove an assignment
// the server would never make, which is how the queue and the census drifted
// apart before (#119). So both halves are asked the same questions here.

func axisLine(w DependencyAxisWork) string {
	return fmt.Sprintf("%s/%s@%s sample=%s score=%d", w.Ecosystem, w.Name, w.Version, w.SampleID, w.Score)
}

func TestIntegrationDependencyAxisFakeMatchesPostgres(t *testing.T) {
	scenarios := []struct {
		name string
		seed []axisSeed
	}{
		{
			// The SE- cell: proven, no tree. The whole reason this axis
			// needed a work kind of its own.
			name: "proven with no dependency answer",
			seed: []axisSeed{
				{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true},
			},
		},
		{
			// Demand orders the queue, and a chosen sighting counts a
			// thousand carried ones on both sides of the comparison.
			name: "demand decides the order",
			seed: []axisSeed{
				{ecosystem: "npm", name: "quiet", version: "1.0.0", proven: true, observations: 50},
				{ecosystem: "npm", name: "chosen", version: "1.0.0", proven: true, observations: 1, direct: true},
			},
		},
		{
			// An ecosystem with no scanner is dropped by the shared Go rule,
			// so the two stores must drop the same rows.
			name: "an unscannable ecosystem is dropped",
			seed: []axisSeed{
				{ecosystem: "maven", name: "com.example/thing", version: "1.0.0", proven: true},
				{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true},
			},
		},
		{
			// A coordinate nobody has proven is not dependency work: there is
			// no sample to re-verify, so the sample axis owns it.
			name: "an unproven coordinate is the sample axis",
			seed: []axisSeed{
				{ecosystem: "npm", name: "unproven", version: "1.0.0", observations: 3},
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := NewFake()
			seedAxis(t, fake, sc.seed)
			pg := openTestPG(t)
			seedAxis(t, pg, sc.seed)

			fakeRows, err := fake.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 20)
			if err != nil {
				t.Fatal(err)
			}
			pgRows, err := pg.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 20)
			if err != nil {
				t.Fatal(err)
			}
			if len(fakeRows) != len(pgRows) {
				t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
					len(fakeRows), len(pgRows), fakeRows, pgRows)
			}
			for i := range pgRows {
				if got, want := axisLine(fakeRows[i]), axisLine(pgRows[i]); got != want {
					t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, got, want)
				}
			}
		})
	}
}

// A sample the fleet is already answering is not asked again, and a sample
// that has spent the ceiling is not asked at all. Both bounds are the
// convergence guarantee, and PostgreSQL derives them from the job table
// rather than from the Go state the Fake keeps -- so they are checked here
// against the real one.
func TestIntegrationDependencyAxisConvergesInPostgres(t *testing.T) {
	ctx := context.Background()
	pg := openTestPG(t)
	seedAxis(t, pg, []axisSeed{{ecosystem: "npm", name: "left-pad", version: "1.3.0", proven: true}})
	sampleID := "sha256:proof-left-pad-1.3.0"

	for i := 0; i < DependencyAxisMaxAttempts; i++ {
		work, err := pg.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(work) != 1 {
			t.Fatalf("attempt %d: work = %d rows, want 1", i, len(work))
		}
		id, err := pg.EnsureCrossJob(ctx, JobRow{SampleID: sampleID, Reason: "cross", Status: "open"})
		if err != nil {
			t.Fatal(err)
		}
		live, err := pg.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(live) != 0 {
			t.Fatalf("attempt %d: a sample with a live verification was offered again", i)
		}
		if err := pg.CompleteJob(ctx, id); err != nil {
			t.Fatal(err)
		}
	}

	work, err := pg.DependencyAxisOpen(ctx, DependencyAxisMaxAttempts, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("work = %d rows after %d attempts: the scheduler never stops asking", len(work), DependencyAxisMaxAttempts)
	}
}

// The evidence axis in the candidate query. A PUBLIC release nothing has ever
// been recorded running was unreachable by construction: every other branch
// arrives through an evidence row keyed by that exact purl, or through a
// sibling already proven.
func TestIntegrationEvidenceAxisFakeMatchesPostgres(t *testing.T) {
	scenarios := []struct {
		name string
		seed []axisSeed
	}{
		{
			name: "a release with nothing recorded on it",
			seed: []axisSeed{
				{ecosystem: "npm", name: "unseen", version: "1.0.0"},
			},
		},
		{
			// Measured demand must come first: evidence-axis work is score 0
			// and ranks last, so nothing anybody asked for is displaced.
			name: "measured demand outranks the evidence axis",
			seed: []axisSeed{
				{ecosystem: "npm", name: "unseen", version: "1.0.0"},
				{ecosystem: "npm", name: "wanted", version: "1.0.0", observations: 10, direct: true},
			},
		},
		{
			// A package already proven at some version belongs to the sibling
			// branch, which reaches its other releases with a target_os a
			// verifier can act on. Offering it here as well would put one
			// coordinate in one window twice under two names.
			name: "a proven sibling keeps its own branch",
			seed: []axisSeed{
				{ecosystem: "npm", name: "half", version: "1.0.0", proven: true, observations: 4},
				{ecosystem: "npm", name: "half", version: "2.0.0"},
			},
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			ctx := context.Background()
			fake := NewFake()
			seedAxis(t, fake, sc.seed)
			pg := openTestPG(t)
			seedAxis(t, pg, sc.seed)

			fakeRows, err := fake.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			pgRows, err := pg.ListAuthoringExpansionCandidates(ctx, 50)
			if err != nil {
				t.Fatal(err)
			}
			if len(fakeRows) != len(pgRows) {
				t.Fatalf("row count differs: fake=%d pg=%d\n fake: %v\n pg:   %v",
					len(fakeRows), len(pgRows), formatCandidateOrder(fakeRows), formatCandidateOrder(pgRows))
			}
			for i := range pgRows {
				if got, want := candidateLine(fakeRows[i]), candidateLine(pgRows[i]); got != want {
					t.Errorf("row %d differs\n  fake: %s\n  pg:   %s", i, got, want)
				}
			}
		})
	}
}
