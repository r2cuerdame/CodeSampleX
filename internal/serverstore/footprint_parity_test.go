package serverstore

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func footprintRow(dedup, sample string, outcome domain.FootprintOutcome) ExecutionFootprintRow {
	return ExecutionFootprintRow{
		DedupKey: dedup,
		SampleID: sample,
		Outcome:  outcome,
		Stage:    domain.FootprintStageBuild,
		Environment: domain.FootprintEnvironment{
			OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22.11.0",
		},
		Epoch:        "2026-09-17",
		SourceBucket: "bucket-a",
	}
}

func runExecutionFootprintStoreContract(t *testing.T, store ExecutionFootprintStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	const sample = "sha256:a1b2c3d4e5f60718293a4b5c6d7e8f9001122334455667788990aabbccddeeff"

	first, duplicate, err := store.RecordExecutionFootprint(ctx, footprintRow("k1", sample, domain.FootprintFail), now)
	if err != nil || duplicate || first.ID == 0 {
		t.Fatalf("first = %+v duplicate=%v err=%v", first, duplicate, err)
	}
	if first.CreatedAt.IsZero() || !first.UpdatedAt.Equal(first.CreatedAt) {
		t.Fatalf("timestamps not set on insert: %+v", first)
	}

	// The same source, sample, stage and day again: one row, and the later
	// outcome stands. A caller that fixed its environment and re-ran is
	// telling us more about the same event.
	again, duplicate, err := store.RecordExecutionFootprint(ctx, footprintRow("k1", sample, domain.FootprintPass), now.Add(time.Hour))
	if err != nil || !duplicate {
		t.Fatalf("duplicate=%v err=%v", duplicate, err)
	}
	if again.ID != first.ID || again.Outcome != domain.FootprintPass {
		t.Fatalf("one event became %d/%d rows or kept the old outcome: %+v", again.ID, first.ID, again)
	}
	if !again.CreatedAt.Equal(first.CreatedAt) || !again.UpdatedAt.After(first.UpdatedAt) {
		t.Fatalf("update did not keep created_at and move updated_at: %+v", again)
	}

	// A different source is a different row, however similar the report.
	if _, dup, err := store.RecordExecutionFootprint(ctx, footprintRow("k2", sample, domain.FootprintCouldNotRun), now); err != nil || dup {
		t.Fatalf("second source: dup=%v err=%v", dup, err)
	}
	other := "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if _, _, err := store.RecordExecutionFootprint(ctx, footprintRow("k3", other, domain.FootprintPass), now); err != nil {
		t.Fatal(err)
	}

	counts, err := store.ExecutionFootprintCounts(ctx, sample)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Pass != 1 || counts.Fail != 0 || counts.CouldNotRun != 1 || counts.Total() != 2 {
		t.Fatalf("counts = %+v, want pass=1 fail=0 couldNotRun=1", counts)
	}

	rows, err := store.ListExecutionFootprints(ctx, 2)
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	if rows[0].ID < rows[1].ID {
		t.Fatalf("list is not newest first: %d then %d", rows[0].ID, rows[1].ID)
	}
	if rows[1].Environment.Runtime != "node" || rows[1].Epoch != "2026-09-17" {
		t.Fatalf("environment or epoch did not round-trip: %+v", rows[1])
	}
}

func TestFakeExecutionFootprintStoreContract(t *testing.T) {
	runExecutionFootprintStoreContract(t, NewFake())
}

func TestIntegrationPGExecutionFootprintStoreMatchesTheFake(t *testing.T) {
	runExecutionFootprintStoreContract(t, openTestPG(t))
}
