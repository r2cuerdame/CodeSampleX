package web

import (
	"strings"
	"testing"
)

// R2C-134. /wanted ranked what people searched for and missed, which is
// demand, not completeness: a coordinate nobody has ever asked about can still
// be the largest hole in the corpus, and one asked about daily can already be
// finished. The page a contributor needs is the one that says what is actually
// missing.
func TestGapsListsWhatIsMissingOnEachAxis(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.gaps = []CompletenessGap{
		{Ecosystem: "npm", Name: "left-pad", Version: "1.3.0",
			HasSample: false, HasEvidence: true, Dependency: "unknown"},
		{Ecosystem: "pypi", Name: "requests", Version: "2.32.3",
			HasSample: true, HasEvidence: true, Dependency: "unknown"},
	}
	body := get(t, mux, "/gaps").Body.String()

	for _, want := range []string{"left-pad", "1.3.0", "requests", "2.32.3"} {
		if !strings.Contains(body, want) {
			t.Errorf("%q missing from the gaps page", want)
		}
	}
	// The state each row is in has to be on the page: a list of incomplete
	// coordinates that does not say WHICH axis is incomplete is the census
	// again, one row at a time.
	if !strings.Contains(body, "-ED") && !strings.Contains(body, "-E-") {
		t.Error("no census cell name reached the page")
	}
}

// The old address keeps working and points at the new one.
//
// /wanted is in old READMEs, old MCP replies and external links. Retiring the
// concept is a decision about what the site says; breaking the URL is a
// decision about whether people arrive at all, and it was not the one made.
func TestWantedRedirectsToGaps(t *testing.T) {
	mux := newTestMuxOnly(t)
	rec := get(t, mux, "/wanted")
	if rec.Code != 301 {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/gaps" {
		t.Errorf("Location = %q, want /gaps", loc)
	}
}

// A gap this network cannot close says so, in the queue's own words.
//
// The census already subtracts these from the backlog. A page that listed
// them as ordinary missing work would hand a contributor a coordinate the
// authoring queue declines on every poll -- the exact disagreement between
// the queue's judgement and the backlog's denominator that #87 was opened for.
func TestGapsSayWhenAnAxisCannotBeClosed(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.gaps = []CompletenessGap{{
		Ecosystem: "npm", Name: "@esbuild/linux-x64", Version: "0.25.0",
		HasEvidence: true, Dependency: "unknown",
		SampleNAReason: "npm per-platform native build: what a sample would import is the .node binary its parent selects",
	}}
	body := get(t, mux, "/gaps").Body.String()
	if !strings.Contains(body, "per-platform native build") {
		t.Error("the reason this axis cannot be closed did not reach the page")
	}
}

// "Resolved, and it declares nothing" and "nobody has looked" are opposite
// facts and must not render as the same blank.
//
// This is the one distinction the dependency axis exists to make. Rendering
// both as an empty cell would have the page assert a measurement that never
// happened, which is the failure this whole project refuses.
func TestGapsSeparateAMeasuredLeafFromAnUnreadOne(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.gaps = []CompletenessGap{
		{Ecosystem: "npm", Name: "unread", Version: "1.0.0",
			HasEvidence: true, Dependency: "unknown"},
		{Ecosystem: "npm", Name: "measured", Version: "1.0.0",
			HasEvidence: true, Dependency: "none"},
	}
	body := get(t, mux, "/gaps?lang=en").Body.String()
	unread := gapRow(t, body, "unread@1.0.0")
	measured := gapRow(t, body, "measured@1.0.0")

	if strings.Contains(unread, "declares nothing") {
		t.Error("a release nobody resolved was rendered as a measured leaf")
	}
	if !strings.Contains(measured, "declares nothing") {
		t.Error("a measured leaf was rendered as if nobody had looked")
	}
	// And the cell names differ, because a measured leaf holds its axis.
	if !strings.Contains(unread, ">-E-<") {
		t.Errorf("unread is not in -E-: %s", unread)
	}
	if !strings.Contains(measured, ">-ED<") {
		t.Errorf("a measured leaf is not in -ED: %s", measured)
	}
}

// gapRow returns the one rendered row holding this coordinate.
//
// Slicing a fixed number of bytes from the name instead measures the markup's
// length rather than the page's meaning: the first version of this test passed
// or failed depending on whether the third axis fell inside a 600-byte window.
func gapRow(t *testing.T, body, coord string) string {
	t.Helper()
	for _, row := range strings.Split(body, `<li class="gaprow">`)[1:] {
		if end := strings.Index(row, "</ol>"); end >= 0 {
			row = row[:end]
		}
		if strings.Contains(row, coord) {
			return row
		}
	}
	t.Fatalf("no gap row for %s", coord)
	return ""
}

// A release nobody has resolved can be traced from its own page to the gap
// list, without the page claiming anything about its dependencies.
//
// The dependency section renders nothing at all in that state, which is
// correct as far as it goes -- a page that said "no dependencies" would be
// asserting a measurement nobody made. But an absent section is also how a
// reader learns nothing, including that this is known work rather than a
// finished coordinate. Saying "nothing has read this yet, here is where it is
// counted" adds the second fact and still makes no claim about the first.
func TestAnUnreadDependencyAxisPointsAtTheGapList(t *testing.T) {
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.versions["npm|silent"] = []string{"1.0.0"}

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/silent?f_version=1.0.0").Body.String()

	if !strings.Contains(body, "/gaps?q=silent") {
		t.Error("an unread dependency axis does not lead to the gap list")
	}
	// And it still refuses the claim the old empty section could not make.
	if strings.Contains(body, "found no dependencies") {
		t.Error("a release nothing read was reported as having none")
	}
}

// A release measured to declare nothing is finished on that axis and must not
// be sent to the gap list.
func TestAMeasuredLeafIsNotSentToTheGapList(t *testing.T) {
	f := newFakeStore()
	f.dependencyEcosystem = "npm"
	f.versions["npm|leaf"] = []string{"1.0.0"}
	f.resolvedNone = map[string]bool{"leaf@1.0.0": true}

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/leaf?f_version=1.0.0").Body.String()
	if strings.Contains(body, "/gaps?q=leaf") {
		t.Error("a closed dependency axis was listed as work left to do")
	}
}

// A release in an ecosystem with no scanner says that, not "not yet".
//
// This is the same distinction /compatibility makes and the census makes.
// "Nothing has resolved this release yet" promises a gap somebody could
// close; for golang, maven, gem, pub, hex and composer nothing here can ever
// resolve it, and pointing that reader at the gap list offers work that does
// not exist.
func TestAnUnscannableEcosystemSaysSoOnThePackagePage(t *testing.T) {
	f := newFakeStore()
	f.dependencyEcosystem = "golang"
	f.versions["golang|example.com/mod"] = []string{"v1.0.0"}

	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/golang/example.com/mod?f_version=v1.0.0&lang=en").Body.String()

	if !strings.Contains(body, "No dependency scanner ships for golang") {
		t.Error("an ecosystem with no scanner was reported as merely unread")
	}
	// Both sentences end "unread rather than empty", deliberately: neither
	// claims the release has no dependencies. What separates them is whether
	// anybody could look, so the assertion is on the opening clause.
	if strings.Contains(body, "Nothing has resolved this release yet") {
		t.Error("an unaskable axis was offered as work not done yet")
	}
	if strings.Contains(body, "/gaps?q=") {
		t.Error("a reader was sent to the gap list for work nothing can do")
	}
}
