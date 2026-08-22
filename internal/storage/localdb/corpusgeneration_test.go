package localdb

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The search engine re-reads and re-parses the whole local corpus on every
// query: 1,770 sample manifests and 974 shard rows holding 12 MB of JSON. It
// can only stop doing that if it can tell, cheaply and without ever being
// wrong, whether the corpus has changed since it last looked.
//
// The counter is kept by triggers rather than by the writers. A cache whose
// invalidation depends on somebody remembering to bump a number is a cache
// that will one day answer from a corpus that has moved, and a stale answer
// here is worse than a slow one. A trigger cannot be forgotten by the next
// person to add a writer.
func TestCorpusGenerationMovesWhenTheCorpusDoes(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	gen := func() int64 {
		t.Helper()
		g, err := db.CorpusGeneration(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}

	start := gen()
	if got := gen(); got != start {
		t.Fatalf("reading twice changed the generation: %d then %d", start, got)
	}

	const sampleID = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := db.SaveSample(ctx, SampleRow{
		SampleID: sampleID, ManifestJSON: `{}`, Status: "LOCAL",
	}); err != nil {
		t.Fatal(err)
	}
	afterInsert := gen()
	if afterInsert == start {
		t.Error("a new sample did not move the generation")
	}

	if err := db.SaveSample(ctx, SampleRow{
		SampleID: sampleID, ManifestJSON: `{"packages":["pkg:npm/x@1.0.0"]}`, Status: "LOCAL_PASS",
	}); err != nil {
		t.Fatal(err)
	}
	afterUpdate := gen()
	if afterUpdate == afterInsert {
		t.Error("changing a sample's manifest and status did not move the generation")
	}

	if err := db.SaveShard(ctx, "npm/axios", "etag-1", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}
	afterShard := gen()
	if afterShard == afterUpdate {
		t.Error("a new shard did not move the generation")
	}

	if err := db.SaveShard(ctx, "npm/axios", "etag-2", `{"schemaVersion":1,"key":"npm/axios"}`); err != nil {
		t.Fatal(err)
	}
	if gen() == afterShard {
		t.Error("re-syncing a shard with new content did not move the generation")
	}
}

// Reading rows must not move it, or the cache never holds.
func TestCorpusGenerationIgnoresReads(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if err := db.SaveSample(ctx, SampleRow{
		SampleID:     "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ManifestJSON: `{}`, Status: "LOCAL",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := db.CorpusGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListSamples(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ListShards(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := db.CorpusGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("listing the corpus moved the generation: %d -> %d", before, after)
	}
}

var _ = domain.PURL{}

// Stamping "this sample was used" must not throw away the search corpus.
//
// last_used lives on the samples table, the triggers are blanket, and
// TouchSample runs on every get_sample and every adoption report — both
// middle steps of the search -> get_sample -> report_adoption -> search loop.
// So the cache was discarded and the whole corpus re-parsed on the next
// search, every time, for a column the search never reads.
//
// The fix is to write less often rather than to narrow the triggers. An
// UPDATE OF list would have to name every column the corpus reads, and the
// day somebody adds one and forgets is the day a stale answer ships — which
// is the failure this design exists to make impossible. Eviction ordering
// needs to know recently-used from not; it does not need seconds.
func TestTouchingASampleDoesNotThrowAwayTheCorpus(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const id = "sha256:" + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err := db.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: `{}`, Status: "LOCAL"}); err != nil {
		t.Fatal(err)
	}

	before, err := db.CorpusGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.TouchSample(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	after, err := db.CorpusGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The first stamp on a never-used sample is a real change and may move it
	// once; the repeats must not.
	if after-before > 1 {
		t.Errorf("five touches moved the generation %d times, want at most 1", after-before)
	}
}

// It still records use — an eviction policy that cannot tell hot from cold
// is worse than a cache that reloads.
func TestTouchingASampleStillRecordsThatItWasUsed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const id = "sha256:" + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if err := db.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: `{}`, Status: "LOCAL"}); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchSample(ctx, id); err != nil {
		t.Fatal(err)
	}
	var lastUsed string
	if err := db.sql.QueryRowContext(ctx,
		`SELECT COALESCE(last_used,'') FROM samples WHERE sample_id = ?`, id).Scan(&lastUsed); err != nil {
		t.Fatal(err)
	}
	if lastUsed == "" {
		t.Error("a sample that was used has no record of it")
	}
}
