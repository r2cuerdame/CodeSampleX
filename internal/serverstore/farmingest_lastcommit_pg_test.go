package serverstore

// Proves LastFarmIngestAt (CSX-453) answers the one question only the server
// can: when did evidence last actually land. It reads MAX(last_seen) over
// evidence_agg -- the column ingestOne updates on every accepted batch --
// through the real ingest write path (IngestBatches), not by inserting
// evidence_agg rows by hand, so this test exercises the exact column the
// method must read.
//
//	$env:CSX_TEST_DSN = "postgres://csx:csx@localhost:5432/csx"
//	go test ./internal/serverstore/ -run TestIntegrationLastFarmIngestAt -v

import (
	"context"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestIntegrationLastFarmIngestAt(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	if _, found, err := pg.LastFarmIngestAt(ctx); err != nil {
		t.Fatalf("LastFarmIngestAt on empty corpus: %v", err)
	} else if found {
		t.Fatal("expected found=false with no evidence_agg rows")
	}

	// obsBatch (merge_test.go) is the fixture the rest of this package's
	// IngestBatches tests already use to build a minimal valid
	// domain.ObservationBatch; reused here rather than duplicated.
	if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{
		obsBatch("anon-lastfarmingest", "proj-lastfarmingest", 1),
	}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("seed ingest: accepted=%d rejected=%v err=%v", accepted, rejected, err)
	}

	got, found, err := pg.LastFarmIngestAt(ctx)
	if err != nil || !found {
		t.Fatalf("LastFarmIngestAt after ingest: found=%v err=%v", found, err)
	}
	if time.Since(got) > time.Minute {
		t.Fatalf("LastFarmIngestAt = %v, want ~now", got)
	}
}
