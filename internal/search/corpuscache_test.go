package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func cachedEngine(t *testing.T) (Engine, *localdb.DB) {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return Engine{DB: db, Corpus: NewCorpusCache()}, db
}

func saveSample(t *testing.T, db *localdb.DB, id, goal string) {
	t.Helper()
	m := domain.SampleManifest{
		SchemaVersion: 1,
		Case:          domain.Case{Goal: goal, Packages: []string{"pkg:npm/axios@1.12.0"}},
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"},
	}
	if err := db.SaveSample(context.Background(), localdb.SampleRow{
		SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(m)), Status: "LOCAL_PASS",
	}); err != nil {
		t.Fatal(err)
	}
}

const sampleA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const sampleB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// The corpus is read once and then held. Reading it is the whole cost of a
// search: 1,770 manifests and 12 MB of shard JSON on the install this was
// measured on, re-parsed for every query with no warm-up at all.
func TestTheCorpusIsReadOnceWhileNothingChanges(t *testing.T) {
	e, db := cachedEngine(t)
	saveSample(t, db, sampleA, "post json with axios")

	for i := 0; i < 5; i++ {
		if _, _, err := e.corpus(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.Corpus.Loads(); got != 1 {
		t.Errorf("read the corpus %d times for five queries, want 1", got)
	}
}

// And it is read again the moment anything moves. A cache that misses this is
// worse than no cache: a stale answer is indistinguishable from a true one.
func TestTheCorpusIsReadAgainWhenItChanges(t *testing.T) {
	e, db := cachedEngine(t)
	saveSample(t, db, sampleA, "post json with axios")

	cands, _, err := e.corpus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("corpus holds %d candidates, want 1", len(cands))
	}

	saveSample(t, db, sampleB, "stream a large download with axios")

	cands, _, err = e.corpus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Errorf("a new sample was not visible: corpus holds %d candidates", len(cands))
	}
	if got := e.Corpus.Loads(); got != 2 {
		t.Errorf("loads = %d, want the change to have forced exactly one reload", got)
	}
}

// An engine with no cache is a working engine. That is what a one-shot CLI
// command gets, and it must not depend on the cache existing.
func TestAnEngineWithoutACacheStillAnswers(t *testing.T) {
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e := Engine{DB: db}
	saveSample(t, db, sampleA, "post json with axios")

	cands, _, err := e.corpus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Errorf("uncached corpus holds %d candidates, want 1", len(cands))
	}
}

// Two searches at once must not see each other's ranking. The FTS score is
// normalised against the best hit of ONE query, and it used to be written
// onto the candidate — which the cache now shares between every query.
func TestConcurrentSearchesDoNotShareOneQuerysRanking(t *testing.T) {
	e, db := cachedEngine(t)
	saveSample(t, db, sampleA, "post json with axios")
	saveSample(t, db, sampleB, "stream a large download with axios")

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			q := "post json"
			if i%2 == 0 {
				q = "stream large download"
			}
			e.Search(context.Background(), domain.SearchRequest{SchemaVersion: 2, Query: q})
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}
