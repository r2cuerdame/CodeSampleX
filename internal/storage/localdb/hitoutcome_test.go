package localdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// TestHitOutcomeSummaryMatchesRowSemantics pins the aggregate to the row
// meaning every surface already uses: adopted>0 is an adoption, an explicit
// applied=false (-1) is not, and a build reported either way is a report.
func TestHitOutcomeSummaryMatchesRowSemantics(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)

	empty, err := db.HitOutcomeSummary(ctx)
	if err != nil || empty != (HitOutcomeCounts{}) {
		t.Fatalf("empty store: %+v err=%v", empty, err)
	}

	ts := time.Now().UTC()
	pass := sql.NullBool{Bool: true, Valid: true}
	fail := sql.NullBool{Bool: false, Valid: true}
	for _, h := range []HitRow{
		{Adopted: true, PostBuildPass: pass},
		{Adopted: true, PostBuildPass: fail},
		{Adopted: true},
		{},
	} {
		h.TS, h.Query, h.Grade, h.SampleID = ts, "q", domain.GradeExact, "sha256:s1"
		if err := db.RecordHit(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	// An applied=false report with a failed build: a build report, not an adoption.
	if err := db.RecordHit(ctx, HitRow{TS: ts, Query: "q", Grade: domain.GradeExact, SampleID: "sha256:s2"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.MarkAdopted(ctx, "sha256:s2", false, fail); err != nil || !ok {
		t.Fatalf("MarkAdopted: ok=%v err=%v", ok, err)
	}

	got, err := db.HitOutcomeSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := HitOutcomeCounts{Hits: 5, Adoptions: 3, PostHitBuildReports: 3, PostHitBuildPasses: 1}
	if got != want {
		t.Fatalf("HitOutcomeSummary = %+v, want %+v", got, want)
	}

	// Same answer as the row-by-row reading ListHits gives.
	rows, err := db.ListHits(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	var fromRows HitOutcomeCounts
	for _, h := range rows {
		fromRows.Hits++
		if h.Adopted {
			fromRows.Adoptions++
		}
		if h.PostBuildPass.Valid {
			fromRows.PostHitBuildReports++
			if h.PostBuildPass.Bool {
				fromRows.PostHitBuildPasses++
			}
		}
	}
	if fromRows != got {
		t.Fatalf("aggregate %+v disagrees with rows %+v", got, fromRows)
	}
}
