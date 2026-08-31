package web

import (
	"strings"
	"testing"
)

// R2C-135. An empty collection has to say it is empty.
//
// A blank page reads as a broken one, and on a network that publishes what it
// has NOT measured as carefully as what it has, "nothing here" is a statement
// worth making rather than an absence to hide. Every collection renders 200
// with something a reader can act on.
func TestEveryEmptyCollectionSaysSo(t *testing.T) {
	for _, path := range []string{"/findings", "/samples", "/compatibility", "/gaps", "/dependencies"} {
		mux, store := newTestMux(t, nil)
		store.sampleList = nil
		store.dependencies = nil
		store.packages = nil
		store.gaps = nil
		store.packageAssets = nil
		store.derived = nil

		rec := get(t, mux, path+"?lang=en")
		if rec.Code != 200 {
			t.Errorf("%s empty: status %d", path, rec.Code)
			continue
		}
		body := rec.Body.String()
		i, j := strings.Index(body, "<main"), strings.Index(body, "</main>")
		if i < 0 || j < i {
			t.Errorf("%s empty: no main content", path)
			continue
		}
		// The <main> holds the heading, the filter bar and the trail even when
		// the collection is empty. What has to be there beyond that is a
		// sentence: a heading over nothing is the blank page with a title.
		main := body[i:j]
		if !strings.Contains(main, "<p") {
			t.Errorf("%s empty: nothing but chrome — no sentence for the reader", path)
		}
		if strings.Contains(main, "unavailable") || strings.Contains(main, "went wrong") {
			t.Errorf("%s empty: rendered as an error", path)
		}
	}
}

// The collections speak the reader's vocabulary, not the schema's.
//
// R2C-135 asks for internal operational vocabulary to leave the public pages.
// /compatibility told visitors that "environment filters match recorded
// snapshot rows" — a snapshot row is a table in this server, and a reader has
// no way to know what one is or why it would match anything.
//
// /features is exempt and always will be: it is the API and MCP reference,
// where purl and snapshot are the names of the things being documented.
func TestCollectionsDoNotSpeakInSchemaTerms(t *testing.T) {
	internal := []string{"snapshot", "purl", "quarantin", "CROSS_PASS", "epoch", "upsert", "backlog"}
	for _, path := range []string{"/findings", "/samples", "/compatibility", "/gaps", "/dependencies"} {
		body := get(t, newTestMuxOnly(t), path+"?lang=en").Body.String()
		i, j := strings.Index(body, "<main"), strings.Index(body, "</main>")
		if i < 0 || j < i {
			t.Fatalf("%s: no main content", path)
		}
		text := stripMarkup(body[i:j])
		for _, term := range internal {
			if strings.Contains(strings.ToLower(text), strings.ToLower(term)) {
				t.Errorf("%s says %q to the reader", path, term)
			}
		}
	}
}

// stripMarkup reduces rendered HTML to the words a visitor reads.
func stripMarkup(h string) string {
	var b strings.Builder
	depth := 0
	for _, r := range h {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
