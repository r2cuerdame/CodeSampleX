package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The collection page prints "Showing 1-24 / 25" over a corpus of 4,683.
//
// The total was never a count. SamplesPage read one row more than a page so it
// could tell whether a next page existed, and returned `offset + len(rows)` —
// a probe value, correct only on the last page. templates/samples.html then
// rendered it as an authoritative "{{.From}}-{{.To}} / {{.Total}}", so every
// page but the last told the reader the collection ends just past where they
// are standing. Walked live: page 1 said 25, page 100 said 2,401, page 196
// said 4,683 and was the only truthful one.
//
// Records and Findings both compute a real total. This is the page that did
// not, and the number a reader uses to decide whether the corpus is worth
// walking is the one that was made up.
func TestTheCollectionCountsWhatItHolds(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	w := &webStore{s: store}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	const corpus = 30
	for i := 0; i < corpus; i++ {
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID:     fmt.Sprintf("sha256:collection-%02d", i),
			ManifestJSON: `{"goal":"prove something","packages":["pkg:npm/axios@1.12.0"]}`,
			Status:       "PUBLISHED",
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}

	rows, total, err := w.SamplesPage(ctx, 0, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 24 {
		t.Errorf("page 1 holds %d rows, want a full page of 24", len(rows))
	}
	if total != corpus {
		t.Errorf("page 1 reports %d samples in the collection, want %d — a reader deciding "+
			"whether to walk it is reading a number nobody counted", total, corpus)
	}

	// The last page has always been right, and must stay right.
	rows, total, err = w.SamplesPage(ctx, 24, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != corpus-24 {
		t.Errorf("page 2 holds %d rows, want %d", len(rows), corpus-24)
	}
	if total != corpus {
		t.Errorf("page 2 reports %d, want %d", total, corpus)
	}
}
