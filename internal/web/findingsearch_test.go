package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// manyDerived builds n derived findings, each distinguishable.
func manyDerived(n int) []DerivedFinding {
	out := make([]DerivedFinding, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, DerivedFinding{
			Ecosystem: "pypi",
			Subject:   fmt.Sprintf("pkg-%d@1.0.0", i),
			Believed:  fmt.Sprintf("belief number %d about the library", i),
			Measured:  fmt.Sprintf("the contract measured outcome %d instead", i),
			SampleID:  fmt.Sprintf("sha256:%064d", i),
		})
	}
	return out
}

// The page grows on its own now, so it has to page. Showing twenty-five of
// four hundred and saying nothing reads as a page that found twenty-five.
func TestFindingsPaginatesTheDerivedGroup(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = manyDerived(80)

	body := get(t, mux, "/findings").Body.String()
	if !strings.Contains(body, "pkg-0@1.0.0") {
		t.Error("first page is missing the first finding")
	}
	if strings.Contains(body, "pkg-30@1.0.0") {
		t.Error("first page is showing an entry that belongs on page two")
	}
	mustContain(t, body, "of 80")   // the range is stated
	mustContain(t, body, "?page=2") // and there is a way forward

	p2 := get(t, mux, "/findings?page=2").Body.String()
	if !strings.Contains(p2, "pkg-25@1.0.0") {
		t.Error("page two does not continue where page one stopped")
	}
	// The hand-written groups lead on every page: they are the editorial
	// substance and they are what a reader landing on page 3 came for.
	mustContain(t, p2, "Contradicts an official source")
}

// A page number past the end is a stale link, not an error.
func TestFindingsPastTheEndRedirects(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = manyDerived(30)
	rec := get(t, mux, "/findings?page=99")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "page=2") {
		t.Errorf("redirected to %q, want the last real page", loc)
	}
}

// The overflow guard /records needed: any browser could panic the page
// with a large ?page=.
func TestFindingsPageNumberCannotOverflow(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = manyDerived(10)
	if rec := get(t, mux, "/findings?page=9223372036854775807"); rec.Code >= 500 {
		t.Fatalf("status = %d: a URL must not be able to break the page", rec.Code)
	}
}

// A query searches every group. Which of three lists a finding lives in is
// an authoring detail, not something a reader knows to filter by.
func TestFindingsSearchCoversEveryGroup(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = []DerivedFinding{{
		Ecosystem: "pypi", Subject: "httpx@0.28.1",
		Believed: "a timeout of five covers the whole request",
		Measured: "connect, read, write and pool each get their own five seconds",
		SampleID: "sha256:" + strings.Repeat("a", 64),
	}}
	body := get(t, mux, "/findings?q=httpx").Body.String()
	mustContain(t, body, "httpx@0.28.1")
	if strings.Contains(body, "bcryptjs") {
		t.Error("a hand-written finding that does not match the query is still shown")
	}

	// And a hand-written one is reachable by the same box.
	hand := get(t, mux, "/findings?q=bcrypt").Body.String()
	mustContain(t, hand, "bcryptjs")
	if strings.Contains(hand, "httpx@0.28.1") {
		t.Error("a derived finding that does not match the query is still shown")
	}
}

// Two words mean both words. Matching either turns a narrowing query into
// a widening one, which is the opposite of what someone typing two words
// is asking for.
func TestFindingsSearchRequiresEveryWord(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = []DerivedFinding{
		{Ecosystem: "npm", Subject: "ws@8.19.0", Believed: "close waits for the peer",
			Measured: "close returns before the frame is acknowledged", SampleID: "sha256:" + strings.Repeat("b", 64)},
		{Ecosystem: "pypi", Subject: "httpx@0.28.1", Believed: "one timeout covers the request",
			Measured: "each phase gets its own timeout", SampleID: "sha256:" + strings.Repeat("c", 64)},
	}
	body := get(t, mux, "/findings?q=pypi+timeout").Body.String()
	mustContain(t, body, "httpx@0.28.1")
	if strings.Contains(body, "ws@8.19.0") {
		t.Error("a finding matching only one of two words was returned")
	}
}

// A query with no match says so, rather than rendering an empty page that
// looks broken.
func TestFindingsSearchWithNoMatchSaysSo(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings?q=zzzznotathing").Body.String()
	mustContain(t, body, "zzzznotathing")
	if strings.Contains(body, "Contradicts an official source") {
		t.Error("a query with no match still rendered the full hand-written list")
	}
}

// Paged and searched views are the same collection sliced differently, so
// they must not compete with the page itself in an index.
func TestFindingsSlicesShareOneCanonical(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = manyDerived(80)
	for _, u := range []string{"/findings", "/findings?page=2", "/findings?q=belief"} {
		body := get(t, mux, u).Body.String()
		mustContain(t, body, `<link rel="canonical" href="https://codesamplex.dev/findings"`)
	}
}
