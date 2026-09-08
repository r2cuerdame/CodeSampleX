package compatibility

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func reopenBuilderTestPG(t *testing.T, conn *pgx.Conn) *serverstore.PG {
	t.Helper()
	var schema string
	if err := conn.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	dsn := conn.Config().ConnString()
	scopedDSN := dsn + " search_path=" + schema
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scopedDSN = u.String()
	}
	pg, err := serverstore.Open(context.Background(), scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pg
}

func builderRepairStats(t *testing.T, pg *serverstore.PG) map[string]any {
	t.Helper()
	raw, exists, err := pg.GetLatestStats(context.Background())
	if err != nil || !exists {
		t.Fatalf("repair stats exists=%v err=%v", exists, err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// A -> legacy B -> full B -> legacy C cannot be repaired from the surviving
// A projection alone. Both live and separately restarted builders must retire B.
func TestIntegrationBuilderProjectionRepairForcesFull(t *testing.T) {
	for _, mode := range []string{"same-instance", "standalone-migrate-restart"} {
		t.Run(mode, func(t *testing.T) {
			pg, conn := openBuilderTestPG(t)
			ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
			seedBuilderPGSample(t, pg, "multi-legacy", []string{"pkg:npm/legacy-a@1.0.0"}, "legacy.call", "")
			freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
			builder := &Builder{Store: pg, Now: func() time.Time { return now }, passes: fullPassEvery}
			if err := builder.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			mutateLegacy := func(purl string) {
				t.Helper()
				if _, err := conn.Exec(ctx, `UPDATE samples SET
					manifest=jsonb_set(manifest,'{packages}',jsonb_build_array($1::text))
					WHERE sample_id='multi-legacy'`, purl); err != nil {
					t.Fatal(err)
				}
			}
			assertShard := func(key string, populated bool) {
				t.Helper()
				_, raw, exists, err := pg.GetShard(ctx, key)
				if err != nil || !exists {
					t.Fatalf("shard %s exists=%v err=%v", key, exists, err)
				}
				var shard Shard
				if err := json.Unmarshal([]byte(raw), &shard); err != nil {
					t.Fatal(err)
				}
				if (len(shard.Packages) > 0) != populated {
					t.Fatalf("shard %s populated=%v, want %v", key, len(shard.Packages) > 0, populated)
				}
			}
			mutateLegacy("pkg:npm/legacy-b@2.0.0")
			builder.passes = fullPassEvery
			if err := builder.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			assertShard("npm/legacy-b/2", true)
			mutateLegacy("pkg:npm/legacy-c@3.0.0")
			before := builderRepairStats(t, pg)
			if pg.BuilderRepairGeneration() != 0 {
				t.Fatal("ordinary writes unexpectedly advanced repair generation")
			}
			if mode == "same-instance" {
				if err := pg.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
				if pg.BuilderRepairGeneration() == 0 {
					t.Fatal("committed projection backfill did not advance generation")
				}
			} else {
				if err := serverstore.Migrate(ctx, conn); err != nil {
					t.Fatal(err)
				}
				if pg.BuilderRepairGeneration() != 0 {
					t.Fatal("standalone migration unexpectedly changed another PG instance")
				}
				pg = reopenBuilderTestPG(t, conn)
				if pg.BuilderRepairGeneration() != 0 {
					t.Fatal("no-op startup migration forced an unnecessary new generation")
				}
				builder = &Builder{Store: pg, Now: func() time.Time { return now }}
			}
			after := builderRepairStats(t, pg)
			if after["builderRepairRequired"] != true {
				t.Fatalf("committed repair did not mark durable full repair: %v", after)
			}
			delete(after, "builderRepairRequired")
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("repair changed honest stats/generatedAt: before=%v after=%v", before, after)
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if err := builder.RunOnce(cancelled); err == nil {
				t.Fatal("cancelled full repair succeeded")
			}
			if builder.completedRepairGeneration != 0 || builderRepairStats(t, pg)["builderRepairRequired"] != true {
				t.Fatal("failed repair consumed the full-repair barrier")
			}
			if err := builder.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			assertShard("npm/legacy-b/2", false)
			assertShard("npm/legacy-c/3", true)
			if builder.completedRepairGeneration != pg.BuilderRepairGeneration() {
				t.Fatal("successful full repair did not acknowledge committed generation")
			}
			if _, marked := builderRepairStats(t, pg)["builderRepairRequired"]; marked {
				t.Fatal("successful full repair left the durable barrier set")
			}
			got := builderPGOutputs(t, conn)
			builder.passes = fullPassEvery
			if err := builder.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if want := builderPGOutputs(t, conn); !reflect.DeepEqual(got, want) {
				t.Fatalf("migration repair/full oracle mismatch:\ngot=%v\nwant=%v", got, want)
			}
		})
	}
}

func TestIntegrationBuilderNoopMigratePreservesResume(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	seedBuilderPGSample(t, pg, "unchanged", []string{"pkg:npm/unchanged@1.0.0"}, "unchanged.call", "")
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	builder := &Builder{Store: pg, Now: func() time.Time { return now }}
	if err := builder.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	before := builderRepairStats(t, pg)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	fresh := reopenBuilderTestPG(t, conn)
	if pg.BuilderRepairGeneration() != 0 || fresh.BuilderRepairGeneration() != 0 {
		t.Fatal("no-op migration invalidated a healthy incremental watermark")
	}
	if after := builderRepairStats(t, fresh); !reflect.DeepEqual(before, after) {
		t.Fatalf("no-op migration changed persisted stats: before=%v after=%v", before, after)
	}
	restarted := &Builder{Store: fresh, Now: func() time.Time { return now }}
	restarted.resumeFromLastCompletedPass(ctx, now)
	if !restarted.lastRun.Equal(now) || restarted.passes != 1 {
		t.Fatalf("healthy restart did not resume: lastRun=%v passes=%d", restarted.lastRun, restarted.passes)
	}
	if err := restarted.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if restarted.passes != 2 {
		t.Fatalf("resumed incremental pass counter=%d, want 2", restarted.passes)
	}
}
