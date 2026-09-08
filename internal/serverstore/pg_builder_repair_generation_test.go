package serverstore

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationBuilderBackfillFailurePreservesCommittedRepairBarrier(t *testing.T) {
	for _, committed := range []int{0, builderProjectionBatch} {
		name := "first-page-rollback"
		if committed > 0 {
			name = "second-page-failure"
		}
		t.Run(name, func(t *testing.T) {
			pg, ctx := openBuilderReadPG(t), context.Background()
			last := committed
			if last == 0 {
				last = 1 // write a valid row before the malformed row in the same page
			}
			if err := pg.SetStatsDaily(ctx, time.Now().UTC().Format("2006-01-02"),
				`{"generatedAt":"2026-09-08T00:00:00Z","retained":17}`); err != nil {
				t.Fatal(err)
			}
			builderSQL(t, pg, func(c *pgx.Conn) error {
				_, err := c.Exec(ctx, `INSERT INTO samples(sample_id,manifest,status,size_bytes)
					SELECT 'failure-'||lpad(i::text,3,'0'),
						jsonb_build_object('packages',jsonb_build_array('pkg:npm/repair@1.0.0'),
							'symbols',CASE WHEN i=$1 THEN '[7]'::jsonb ELSE '["call"]'::jsonb END),
						'CROSS_PASS',0 FROM generate_series(0,$1::int) i`, last)
				return err
			})
			err := pg.Migrate(ctx)
			if err == nil || !strings.Contains(err.Error(), "ambiguous builder sample projection") {
				t.Fatalf("malformed backfill result=%v", err)
			}
			wantGeneration := uint64(0)
			if committed > 0 {
				wantGeneration = 1
			}
			if got := pg.BuilderRepairGeneration(); got != wantGeneration {
				t.Fatalf("failed backfill generation=%d want=%d", got, wantGeneration)
			}
			raw, exists, err := pg.GetLatestStats(ctx)
			if err != nil || !exists {
				t.Fatalf("stats exists=%v err=%v", exists, err)
			}
			var stats map[string]any
			if err := json.Unmarshal([]byte(raw), &stats); err != nil {
				t.Fatal(err)
			}
			marked := stats["builderRepairRequired"] == true
			if marked != (committed > 0) {
				t.Fatalf("repair flag=%v after %d committed rows", marked, committed)
			}
			delete(stats, "builderRepairRequired")
			if !reflect.DeepEqual(stats, map[string]any{"generatedAt": "2026-09-08T00:00:00Z", "retained": float64(17)}) {
				t.Fatalf("backfill changed unrelated stats: %v", stats)
			}
			builderSQL(t, pg, func(c *pgx.Conn) error {
				var ready int
				if err := c.QueryRow(ctx, "SELECT count(*) FROM samples WHERE builder_source_hash IS NOT NULL").Scan(&ready); err != nil {
					return err
				}
				if ready != committed {
					t.Fatalf("failed page committed %d source projections, want %d", ready, committed)
				}
				_, err := c.Exec(ctx, `UPDATE samples SET manifest=jsonb_set(manifest,'{symbols}','["call"]')
					WHERE builder_source_hash IS NULL`)
				return err
			})
			if err := pg.Migrate(ctx); err != nil {
				t.Fatalf("resume corrected backfill: %v", err)
			}
			if got := pg.BuilderRepairGeneration(); got != wantGeneration+1 {
				t.Fatalf("resumed generation=%d want=%d", got, wantGeneration+1)
			}
			if err := pg.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if got := pg.BuilderRepairGeneration(); got != wantGeneration+1 {
				t.Fatalf("no-op migration advanced resumed generation to %d", got)
			}
			t.Logf("PASS: failed backfill retained %d committed rows, generation=%d, durable flag=%v; corrected page resumed", committed, wantGeneration, marked)
		})
	}
}
