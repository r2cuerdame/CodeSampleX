package web

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A goal is a sentence somebody wrote, and the queue writes most of them:
// "verify <symbol> in pkg:<eco>/<name>@<version>". Some of those are a single
// unbroken 175-character token — a Go import path plus a pseudo-version has no
// space to break at — and the line ran straight out of the card on the live
// page.
//
// Reported from production on 2026-08-30 against
// a.sample-card__goal, whose text was
// "verify google.golang.org/genproto/googleapis/rpc/context/attribute_context
// .AttributeContext in pkg:golang/google.golang.org/genproto/googleapis/rpc@
// v0.0.0-20260825221802-da73d73af1c5".
//
// Two things were wrong and both are fixed. The card already prints that
// release and that symbol on the line below, so the suffix said the same thing
// twice in the one place a reader scans first — and it is the half that
// overflowed.
func TestTheCardTitleDoesNotRepeatTheCoordinateItAlreadyShows(t *testing.T) {
	long := "verify google.golang.org/genproto/googleapis/rpc/context/attribute_context.AttributeContext" +
		" in pkg:golang/google.golang.org/genproto/googleapis/rpc@v0.0.0-20260825221802-da73d73af1c5"
	got := sampleGoalHeadline(long)
	if strings.Contains(got, "pkg:") {
		t.Errorf("the coordinate is still in the headline: %q", got)
	}
	if !strings.HasPrefix(got, "verify google.golang.org/genproto") {
		t.Errorf("the headline lost what the sample is about: %q", got)
	}

	// A goal an author wrote themselves has no such suffix and is left alone.
	human := "iterate seek and delete key-value pairs with bbolt Cursor"
	if sampleGoalHeadline(human) != human {
		t.Errorf("a hand-written goal was trimmed: %q", sampleGoalHeadline(human))
	}
	// And "in pkg:" appearing mid-sentence in a real goal is not a suffix to
	// cut at the first occurrence — the trim takes the LAST one.
	odd := "explain what in pkg: means in pkg:npm/axios@1.12.0"
	if got := sampleGoalHeadline(odd); got != "explain what in pkg: means" {
		t.Errorf("trimmed at the wrong occurrence: %q", got)
	}
}

// Whatever survives the trim still has to fit. A single long token cannot be
// broken by wrapping words, so the card has to be told to break inside one.
func TestTheCardTitleIsAllowedToBreakInsideALongToken(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("static", "site.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	i := strings.Index(css, ".sample-card__goal {")
	if i < 0 {
		t.Fatal("the card title has no rule of its own")
	}
	rule := css[i:]
	if j := strings.Index(rule, "}"); j > 0 {
		rule = rule[:j]
	}
	if !strings.Contains(rule, "overflow-wrap: anywhere") {
		t.Errorf("a 175-character path with no space in it will run out of the card:\n%s", rule)
	}
	if !strings.Contains(rule, "min-width: 0") {
		t.Errorf("a grid item defaults to min-content width and will not shrink:\n%s", rule)
	}
}

// A reader looking for a reusable answer types a package name or an API. One
// box searches the goal, the packages and the symbols, because making them
// pick a field first is asking them to know the schema.
func TestSamplesCanBeSearched(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	body := get(t, mux, "/samples").Body.String()
	if !strings.Contains(body, `name="q"`) {
		t.Fatal("the sample collection has no search box")
	}

	// A query that matches nothing says so, and offers the way back.
	miss := get(t, mux, "/samples?q=zzz-nothing-matches-this-zzz").Body.String()
	if !strings.Contains(miss, `href="/samples"`) {
		t.Error("a search that found nothing leaves the reader with no way back to the collection")
	}
}

// The query survives paging. Dropping it would answer "page 2 of your search"
// with the whole collection, which is a different question.
func TestPagingKeepsTheSearch(t *testing.T) {
	if got := samplesHref("axios", 2, "ko"); !strings.Contains(got, "q=axios") || !strings.Contains(got, "page=2") {
		t.Errorf("samplesHref = %q, want the query and the page kept", got)
	}
	if got := samplesHref("", 1, "en"); got != "/samples" {
		t.Errorf("the plain collection link is %q, want /samples", got)
	}
}

// A stale deep link should land the reader on the last real page, the way
// /records, /findings and /wanted all do. /samples alone sent them back to
// page 1 with no explanation — so a bookmark into a collection that has since
// grown quietly returns the newest samples instead of the ones near where the
// reader was standing, and nothing on the page says it moved them.
func TestAStalePageLandsOnTheLastRealPage(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.sampleList = nil
	for i := 0; i < samplesPerPage+6; i++ { // two pages, the second part-full
		f.sampleList = append(f.sampleList, SampleListItem{
			SampleID: fmt.Sprintf("sha256:stale-%02d", i),
			Goal:     "prove something",
		})
	}

	res := get(t, mux, "/samples?page=99")
	if res.Code != http.StatusFound {
		t.Fatalf("status = %d, want a redirect for a page past the end", res.Code)
	}
	if got := res.Header().Get("Location"); got != samplesHref("", 2, "en") {
		t.Errorf("Location = %q, want the last real page %q", got, samplesHref("", 2, "en"))
	}
}
