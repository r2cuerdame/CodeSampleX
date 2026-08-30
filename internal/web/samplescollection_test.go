package web

import (
	"strings"
	"testing"
)

// A sample nobody can reach is a sample nobody can reuse. Until this route
// existed the only way to one was a link from the package it happens to be
// about, so a reader who wanted to see what this network had actually built
// had nowhere to go — and the global navigation offered Records and Findings
// but not the samples themselves.
func TestTheSampleCollectionIsReachableFromTheGlobalNavigation(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	body := get(t, mux, "/").Body.String()
	if !strings.Contains(body, `href="/samples"`) {
		t.Error("the front page's navigation offers no way to the sample collection")
	}

	rec := get(t, mux, "/samples")
	if rec.Code != 200 {
		t.Fatalf("/samples status = %d, want 200", rec.Code)
	}
}

// The card leads with what the sample ANSWERS. A sample id is a content hash,
// and a page of hashes tells a reader nothing about which answer is worth
// reusing.
func TestASampleCardLeadsWithItsAnswerNotItsHash(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples").Body.String()

	goal := strings.Index(body, `class="sample-card__goal"`)
	if goal < 0 {
		t.Fatal("no sample card rendered its goal")
	}
	// The id may still appear — in the href — but the goal is what the reader
	// meets first inside the card.
	coord := strings.Index(body, `class="sample-card__coord`)
	if coord >= 0 && coord < goal {
		t.Error("the coordinate is rendered above the answer the sample gives")
	}
}

// A page past the end is a stale link, not an error screen.
func TestAPagePastTheEndGoesBackToTheFirst(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/samples?page=400")
	if rec.Code != 302 {
		t.Fatalf("status = %d, want a redirect to the first page", rec.Code)
	}
}

// ?page= is multiplied before it reaches the store. Atoi happily returns
// 9223372036854775807, and (page-1)*samplesPerPage overflows to a negative
// offset the store would slice with — the same URL-crashes-the-page shape the
// records collection was fixed for.
func TestAHugePageNumberCannotOverflowTheOffset(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/samples?page=9223372036854775807")
	if rec.Code >= 500 {
		t.Fatalf("status = %d: a page number crashed the collection", rec.Code)
	}
}
