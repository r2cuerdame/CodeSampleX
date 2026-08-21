package search

import (
	"context"
	"sync"
	"sync/atomic"
)

// CorpusCache holds the parsed local corpus between queries.
//
// The engine re-read and re-parsed all of it on every single query: 1,770
// sample manifests and 974 shard rows holding 12 MB of JSON on the install
// this was measured on, with no warm-up curve at all — three identical
// queries back to back cost the same each time.
//
// Nothing in that work depends on the question. What does depend on the
// question is the FTS score, and that is why it no longer lives on the
// candidate: a value normalised against one query's best hit must not be
// read by the next, and two searches at once must not write the same field.
//
// A nil cache is a working engine that simply reloads each time, which is
// what the CLI wants for a single command.
type CorpusCache struct {
	mu         sync.Mutex
	loaded     bool
	generation int64
	cands      map[string]*candidate
	evidence   map[string]*pkgEvidence

	// loads counts how many times the corpus was actually read from the
	// database. A cache that silently stops holding is a performance bug
	// nobody notices, so it is countable.
	loads atomic.Int64
}

// NewCorpusCache returns a cache ready to be shared by every search a
// long-lived process runs.
func NewCorpusCache() *CorpusCache { return &CorpusCache{} }

// Loads reports how many times the corpus has been read from the database.
func (c *CorpusCache) Loads() int64 {
	if c == nil {
		return 0
	}
	return c.loads.Load()
}

// corpus returns the parsed corpus, reading it only when the local database
// says something in it has moved.
//
// The generation is read again after the load. A writer that commits while
// we are parsing would otherwise leave a corpus tagged with the generation it
// started at — correct now, and served as fresh until the NEXT change. When
// that happens the result is used but not kept.
func (e Engine) corpus(ctx context.Context) (map[string]*candidate, map[string]*pkgEvidence, error) {
	c := e.Corpus
	gen, genErr := e.DB.CorpusGeneration(ctx)
	if genErr != nil {
		// Cannot tell whether it moved, so do not pretend it has not.
		c = nil
	}

	if c != nil {
		c.mu.Lock()
		if c.loaded && c.generation == gen {
			cands, evidence := c.cands, c.evidence
			c.mu.Unlock()
			return cands, evidence, nil
		}
		c.mu.Unlock()
	}

	if e.Corpus != nil {
		e.Corpus.loads.Add(1)
	}
	cands, evidence, err := e.loadCorpus(ctx)
	if err != nil {
		return nil, nil, err
	}
	if c == nil {
		return cands, evidence, nil
	}

	after, afterErr := e.DB.CorpusGeneration(ctx)
	if afterErr != nil || after != gen {
		// It moved underneath us. The result is still a coherent read of
		// SOME state and is fine to answer from, but it must not be kept.
		return cands, evidence, nil
	}
	c.mu.Lock()
	c.loaded, c.generation, c.cands, c.evidence = true, gen, cands, evidence
	c.mu.Unlock()
	return cands, evidence, nil
}
