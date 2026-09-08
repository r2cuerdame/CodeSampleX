package compatibility

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type correctionRepairStore struct {
	*serverstore.PG
	fullReads int
}

func (s *correctionRepairStore) ListSamplesPage(ctx context.Context, limit, offset int) ([]serverstore.SampleRow, error) {
	s.fullReads++
	return s.PG.ListSamplesPage(ctx, limit, offset)
}

// A full pass may publish a source whose projection is blocked. Correcting that
// source must require full reconciliation: intervening legacy overwrites may
// have erased source attribution after its snapshot/shard was already published.
func TestIntegrationBuilderSampleCorrectionRetiresUntrustedSource(t *testing.T) {
	for _, writer := range []string{"aliases", "legacy-sql", "legacy-sql-overwrites"} {
		t.Run(writer, func(t *testing.T) {
			pg, conn := openBuilderTestPG(t)
			ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
			seedBuilderPGSample(t, pg, "moving", []string{"pkg:npm/initial@1.0.0"}, "initial.call",
				"pkg:npm/initial-subject@1.0.0", []string{"pkg:npm/receipt-harness@1.0.0"})
			full := &Builder{Store: pg, Now: func() time.Time { return now }}
			if err := full.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			row, _, err := pg.GetSample(ctx, "moving")
			if err != nil {
				t.Fatal(err)
			}
			var manifest domain.SampleManifest
			if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Packages = []string{"pkg:npm/intermediate@2.0.0"}
			manifest.Subject = "pkg:npm/intermediate-subject@2.0.0"
			manifest.Symbols = []string{"intermediate.call"}
			row.ManifestJSON = string(domain.MustCanonicalJSON(manifest))
			if writer == "aliases" {
				row.ManifestJSON = strings.NewReplacer(
					"\"packages\":", "\"Packages\":", "\"symbols\":", "\"Symbols\":",
					"\"subject\":", "\"Subject\":").Replace(row.ManifestJSON)
				err = pg.SaveSample(ctx, row)
			} else {
				_, err = conn.Exec(ctx, "UPDATE samples SET manifest=$1 WHERE sample_id='moving'", []byte(row.ManifestJSON))
			}
			if err != nil {
				t.Fatal(err)
			}
			full.passes = fullPassEvery
			if err := full.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if _, exists, err := pg.GetSnapshot(ctx, manifest.Subject, "intermediate.call"); err != nil || !exists {
				t.Fatalf("intermediate full-pass snapshot exists=%v err=%v", exists, err)
			}
			freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
			if writer == "legacy-sql-overwrites" {
				manifest.Packages = []string{"pkg:npm/erased-source@8.0.0"}
				manifest.Subject = "pkg:npm/erased-subject@8.0.0"
				manifest.Symbols = []string{"erased.call"}
				if _, err := conn.Exec(ctx, "UPDATE samples SET manifest=$1 WHERE sample_id='moving'", domain.MustCanonicalJSON(manifest)); err != nil {
					t.Fatal(err)
				}
			}
			generation := pg.BuilderRepairGeneration()
			manifest.Packages = []string{"pkg:npm/final@3.0.0"}
			manifest.Subject = "pkg:npm/final-subject@3.0.0"
			manifest.Symbols = []string{"final.call"}
			row.ManifestJSON = string(domain.MustCanonicalJSON(manifest))
			before, _, err := pg.GetSample(ctx, "moving")
			if err != nil {
				t.Fatal(err)
			}
			if err := pg.SaveSample(ctx, row); err == nil || !strings.Contains(err.Error(), "refusing to overwrite untrusted builder sample moving") {
				t.Fatalf("online overwrite did not fail closed: %v", err)
			}
			after, _, err := pg.GetSample(ctx, "moving")
			if err != nil || !reflect.DeepEqual(before, after) || pg.BuilderRepairGeneration() != generation {
				t.Fatalf("rejected overwrite changed source/generation: before=%+v after=%+v err=%v", before, after, err)
			}
			// Explicit operator repair occurs offline. Migration sets a durable
			// full-repair barrier because overwritten legacy history is unknown.
			if _, err := conn.Exec(ctx, "UPDATE samples SET manifest=$1 WHERE sample_id='moving'", []byte(row.ManifestJSON)); err != nil {
				t.Fatal(err)
			}
			if err := pg.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			if pg.BuilderRepairGeneration() != generation+1 {
				t.Fatal("offline source correction did not require full repair after commit")
			}
			if _, err := pg.BuilderChangesSince(ctx, now.Add(-5*time.Minute)); err == nil {
				t.Fatal("durable repair barrier allowed scoped acquisition before full reconciliation")
			}
			store := &correctionRepairStore{PG: pg}
			repair := &Builder{Store: store, Now: func() time.Time { return now }, lastRun: now.Add(-5 * time.Minute), passes: 1}
			if err := repair.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if store.fullReads == 0 {
				t.Fatal("untrusted source correction resumed incrementally instead of repairing lost history")
			}
			if _, err := pg.BuilderChangesSince(ctx, now.Add(-5*time.Minute)); err != nil {
				t.Fatalf("successful full reconciliation did not unblock incremental acquisition: %v", err)
			}
			if _, exists, err := pg.GetSnapshot(ctx, "pkg:npm/intermediate-subject@2.0.0", "intermediate.call"); err != nil || exists {
				t.Fatalf("corrected source still serves intermediate snapshot: exists=%v err=%v", exists, err)
			}
			for _, key := range []string{"npm/intermediate/2", "npm/intermediate-subject/2"} {
				_, raw, exists, err := pg.GetShard(ctx, key)
				if err != nil || !exists {
					t.Fatalf("intermediate shard %s exists=%v err=%v", key, exists, err)
				}
				var shard Shard
				if err := json.Unmarshal([]byte(raw), &shard); err != nil {
					t.Fatal(err)
				}
				if len(shard.Packages) != 0 {
					t.Fatalf("corrected source still serves intermediate shard %s: %s", key, raw)
				}
			}
			got := builderPGOutputs(t, conn)
			full.passes = fullPassEvery
			if err := full.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			if want := builderPGOutputs(t, conn); !reflect.DeepEqual(got, want) {
				t.Fatalf("source correction incremental/full mismatch:\ngot=%v\nwant=%v", got, want)
			}
		})
	}
}

func TestIntegrationBuilderLiteralPercentSourceParity(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	seedBuilderPGSample(t, pg, "literal", []string{"pkg:npm/%2540foo@1.0.0"}, "literal.call", "")
	seedBuilderPGSample(t, pg, "escaped", []string{"pkg:npm/%40foo@1.0.0"}, "escaped.call", "")
	full := &Builder{Store: pg, Now: func() time.Time { return now }}
	if err := full.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	readShard := func(key string) Shard {
		t.Helper()
		_, raw, exists, err := pg.GetShard(ctx, key)
		if err != nil || !exists {
			t.Fatalf("literal-percent shard %s exists=%v err=%v", key, exists, err)
		}
		var out Shard
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if len(readShard("npm/%40foo/1").Packages) != 1 {
		t.Fatal("full builder did not establish distinct literal-percent source")
	}
	for _, mutate := range []bool{false, true} {
		freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
		row, _, err := pg.GetSample(ctx, "literal")
		if err != nil {
			t.Fatal(err)
		}
		row.HotScore = 50
		if mutate {
			var manifest domain.SampleManifest
			if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
				t.Fatal(err)
			}
			manifest.Packages = []string{"pkg:npm/%2561lias@2.0.0"}
			row.ManifestJSON = string(domain.MustCanonicalJSON(manifest))
		}
		if err := pg.SaveSample(ctx, row); err != nil {
			t.Fatal(err)
		}
		scoped := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-5 * time.Minute), passes: 1}
		if err := scoped.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		got := builderPGOutputs(t, conn)
		full.passes = fullPassEvery
		if err := full.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if want := builderPGOutputs(t, conn); !reflect.DeepEqual(got, want) {
			t.Fatalf("literal-percent mutation=%v incremental/full mismatch:\ngot=%v\nwant=%v", mutate, got, want)
		}
	}
	if len(readShard("npm/%40foo/1").Packages) != 0 || len(readShard("npm/%61lias/2").Packages) != 1 {
		t.Fatal("literal-percent mutation failed to retire old and create new source shard")
	}
	if len(readShard("npm/@foo/1").Packages) != 1 {
		t.Fatal("literal-percent mutation changed the distinct decoded scoped name")
	}
}
