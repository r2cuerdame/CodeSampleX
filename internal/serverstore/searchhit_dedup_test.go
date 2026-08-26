package serverstore

import (
	"context"
	"testing"
)

// A retry of one queued upload reuses its offer ID. Repeating the search runs
// RecordSearchOffer again and gets another offer ID, so it is new demand even
// when every other field is identical.
func TestSearchHitDedupSeparatesTransportRetryFromRepeatedSearch(t *testing.T) {
	store := NewFake()
	first := SearchHitRow{
		Grade: "EXACT", ResultsShown: 1, SampleID: "sha256:sample",
		OfferID: "offer-first", Epoch: "2026-08-26", AnonID: "anon-day",
	}
	if err := store.RecordSearchHit(context.Background(), first); err != nil {
		t.Fatal(err)
	}

	retry := first
	retry.ResultsShown = 2
	if err := store.RecordSearchHit(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	if got := len(store.searchHits); got != 1 {
		t.Fatalf("transport retry rows = %d, want 1", got)
	}

	repeatedSearch := first
	repeatedSearch.OfferID = "offer-second"
	if err := store.RecordSearchHit(context.Background(), repeatedSearch); err != nil {
		t.Fatal(err)
	}
	if got := len(store.searchHits); got != 2 {
		t.Fatalf("repeated-search rows = %d, want 2 distinct offers", got)
	}
}
