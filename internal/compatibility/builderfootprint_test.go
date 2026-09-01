package compatibility

import (
	"context"
	"strings"
	"testing"
)

// Cluster evidence is assembled one package at a time.
//
// It used to be built for every package at once, alongside a dedup index the
// same size, while byPkg still held the whole corpus from the snapshot stage.
// A full pass therefore carried the corpus roughly three times over.
//
// Measured on production 2026-09-01: the server was OOM-killed eight times,
// anon-rss 694MB against a 768MiB limit, on a five-minute cycle. evidence_agg
// had gone from roughly 72k rows to 216k that day. The kill leaves lastRun
// zero and RunOnce forces a full pass whenever lastRun is zero, so every
// restart rebuilt the same map and died the same way -- the loop that kept
// the deploy's builder-freshness gate from ever being satisfied.
//
// The shape is what this pins. A function that returns evidence for EVERY
// package at once cannot be bounded by anything, however fast it is.
func TestClusterEvidenceIsGatheredPerPackage(t *testing.T) {
	src := readBuilderSource(t)
	if strings.Contains(src, "func (b *Builder) evidenceForPackages(") {
		t.Error("cluster evidence is gathered for every package at once; a full pass " +
			"then holds the corpus a second time, with its dedup index a third")
	}
	if !strings.Contains(src, "func (b *Builder) evidenceForPackage(") {
		t.Fatal("the per-package gatherer is gone")
	}
	// byPkg is NOT released here, and that is deliberate rather than an
	// oversight: regenerateShards reads it after this stage. An attempt to
	// free it per package emptied the map and the pass produced no shards at
	// all -- caught by TestStartBuilderRunsPipeline. Bounding that copy too
	// means restructuring the whole pass around one package at a time, which
	// is a larger change than this one and is not pretended to be done.
	if !strings.Contains(src, "b.regenerateShards(ctx, byPkg,") {
		t.Error("the shard stage no longer reads byPkg; the note above is stale " +
			"and the release this test used to forbid may now be possible")
	}
}

// The dedup that a per-package gather must not lose: one symbol filed under
// two spellings is still counted once. This is the production regression that
// made a cluster of 520 out of 260 observed failures, and the per-package
// refactor moves the index that prevents it.
func TestPerPackageGatherStillDedupesSpellings(t *testing.T) {
	// The behaviour itself is covered end-to-end by
	// TestOneSymbolFiledUnderTwoSpellingsIsCountedOnce against a real store.
	// This asserts the index the refactor moved is still per package and
	// still keyed by row identity rather than by arrival.
	src := readBuilderSource(t)
	fn := src[strings.Index(src, "func (b *Builder) evidenceForPackage("):]
	fn = fn[:strings.Index(fn, "\n}\n")]
	if !strings.Contains(fn, "seen := map[evidenceKey]bool{}") {
		t.Error("the per-package gatherer no longer dedupes by row identity")
	}
	if strings.Contains(fn, "map[pkgKey]map[evidenceKey]bool") {
		t.Error("the dedup index is still corpus-sized")
	}
	_ = context.Background
}
