package compatibility

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Changing source content is different from receipt arrival/quarantine: the
// old source-only shard has no remaining target from which to recover its key.
func TestIntegrationBuilderSampleMutationRetiresPreviousSourcesAndReranks(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Second)
	seedBuilderPGSample(t, pg, "z-moving-source", []string{"pkg:npm/old-source@1.0.0"}, "old.source", "")
	seedBuilderPGSample(t, pg, "a-ranking-peer", []string{"pkg:npm/new-source@3.0.0"}, "new.source", "")
	seedBuilderPGSample(t, pg, "moving-subject", []string{"pkg:npm/harness@1.0.0"}, "old.subject",
		"pkg:npm/old-subject@2.0.0", []string{"pkg:npm/harness@1.0.0"})
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	full := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-time.Hour), passes: fullPassEvery}
	if err := full.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	readShard := func(key string) Shard {
		t.Helper()
		_, raw, exists, err := pg.GetShard(ctx, key)
		if err != nil || !exists {
			t.Fatalf("shard %s exists=%v err=%v", key, exists, err)
		}
		var shard Shard
		if err := json.Unmarshal([]byte(raw), &shard); err != nil {
			t.Fatal(err)
		}
		return shard
	}
	for _, key := range []string{"npm/old-source/1", "npm/old-subject/2"} {
		if len(readShard(key).Packages) == 0 {
			t.Fatalf("old source fixture %s was empty", key)
		}
	}
	if _, exists, err := pg.GetSnapshot(ctx, "pkg:npm/old-subject@2.0.0", "old.subject"); err != nil || !exists {
		t.Fatalf("old subject snapshot fixture exists=%v err=%v", exists, err)
	}
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	for _, mutation := range []struct {
		id, purl, symbol string
		subject          bool
	}{
		{"z-moving-source", "pkg:npm/new-source@3.0.0", "new.source", false},
		{"moving-subject", "pkg:npm/new-subject@9.0.0", "new.subject", true},
	} {
		row, exists, err := pg.GetSample(ctx, mutation.id)
		if err != nil || !exists {
			t.Fatalf("sample %s exists=%v err=%v", mutation.id, exists, err)
		}
		var manifest domain.SampleManifest
		if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
			t.Fatal(err)
		}
		if mutation.subject {
			manifest.Subject = mutation.purl
		} else {
			manifest.Packages = []string{mutation.purl}
		}
		manifest.Symbols = []string{mutation.symbol}
		row.ManifestJSON = string(domain.MustCanonicalJSON(manifest))
		if err := pg.SaveSample(ctx, row); err != nil {
			t.Fatal(err)
		}
		var updated time.Time
		if err := conn.QueryRow(ctx, "SELECT updated_at FROM samples WHERE sample_id=$1", mutation.id).Scan(&updated); err != nil {
			t.Fatal(err)
		}
		if !updated.After(now.Add(-5 * time.Minute)) {
			t.Fatalf("SaveSample %s did not advance mutation clock: %v", mutation.id, updated)
		}
	}
	runScopedAndCompareFull := func() {
		t.Helper()
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
			t.Fatalf("source mutation incremental/full mismatch:\ngot=%v\nwant=%v", got, want)
		}
	}
	runScopedAndCompareFull()
	for _, key := range []string{"npm/old-source/1", "npm/old-subject/2"} {
		if len(readShard(key).Packages) != 0 {
			t.Fatalf("old source shard %s still serves withdrawn content", key)
		}
	}
	if _, exists, err := pg.GetSnapshot(ctx, "pkg:npm/old-subject@2.0.0", "old.subject"); err != nil || exists {
		t.Fatalf("old subject snapshot not retired: exists=%v err=%v", exists, err)
	}
	if _, exists, err := pg.GetSnapshot(ctx, "pkg:npm/new-subject@9.0.0", "new.subject"); err != nil || !exists {
		t.Fatalf("new subject snapshot absent: exists=%v err=%v", exists, err)
	}
	samples := readShard("npm/new-source/3").Packages[0].Samples
	if len(samples) != 2 || samples[0].SampleID != "a-ranking-peer" {
		t.Fatalf("unranked source fixture=%+v", samples)
	}
	// A hot-score-only update preserves the source hash, yet must still dirty
	// and rebuild the package. No receipt or source timestamp may mask it.
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	row, _, err := pg.GetSample(ctx, "z-moving-source")
	if err != nil {
		t.Fatal(err)
	}
	row.HotScore = 100
	if err := pg.SaveSample(ctx, row); err != nil {
		t.Fatal(err)
	}
	runScopedAndCompareFull()
	samples = readShard("npm/new-source/3").Packages[0].Samples
	if len(samples) != 2 || samples[0].SampleID != "z-moving-source" {
		t.Fatalf("hot-score-only mutation did not rerank exact shard output: %+v", samples)
	}
}
