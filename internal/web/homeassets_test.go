package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// R2C-139. The front page led with inventory counters -- observations,
// samples, packages. Those say how big this network is, which is a fact about
// the network; a visitor is asking what it found out.
//
// The acceptance is concrete: one screen, one real finding, one reusable
// sample, one dependency insight. This asserts all three reach the page and
// that they come before the counters' supporting material.
func TestTheFrontPageLeadsWithWhatWasFound(t *testing.T) {
	body := warmHome(t, func(f *fakeStore) {
		f.sampleList = []SampleListItem{{
			SampleID: "sha256:aaa", Goal: "verify zod coerce.number rejects an empty string",
			Ecosystem: "npm", Name: "zod", Version: "4.4.3", Symbols: []string{"z.coerce.number"},
			Context: "node 22", Status: "PUBLISHED",
		}}
		f.dependencyEcosystem = "npm"
		f.dependencies = []DependencyEdge{
			{ParentName: "browserslist", ParentVersion: "4.28.8", ChildName: "node-releases", ChildVersion: "2.0.54"},
			{ParentName: "browserslist", ParentVersion: "4.28.7", ChildName: "node-releases", ChildVersion: "2.0.53"},
		}
	})

	// A reusable sample, named by its goal rather than its hash.
	if !strings.Contains(body, "verify zod coerce.number rejects an empty string") {
		t.Error("no reusable sample on the front page")
	}
	if !strings.Contains(body, "/samples/sha256:aaa") {
		t.Error("the sample does not link to itself")
	}
	// A dependency insight.
	if !strings.Contains(body, "node-releases") || !strings.Contains(body, "browserslist") {
		t.Error("no dependency insight on the front page")
	}
	// A finding, which the page already had.
	if !strings.Contains(body, `id="measured"`) {
		t.Error("no measured finding on the front page")
	}
	// Order: what was found comes before the compatibility grid that supports
	// it. A page that opens with its own inventory is a dashboard.
	measured := strings.Index(body, `id="measured"`)
	samples := strings.Index(body, `id="samples"`)
	moved := strings.Index(body, `id="moved"`)
	matrix := strings.Index(body, `id="matrix"`)
	if !(measured < samples && samples < moved && moved < matrix) {
		t.Errorf("sections are out of order: measured=%d samples=%d moved=%d matrix=%d",
			measured, samples, moved, matrix)
	}
}

// An empty strip does not render, and never renders a placeholder.
//
// Showing real data only is the front page's whole claim. A sample-shaped box
// with nothing in it is this network inventing activity, which is the one
// thing it must not do on the page that asks to be trusted.
func TestTheFrontPageShowsNoEmptyStrips(t *testing.T) {
	// Emptied explicitly: the shared fake seeds a sample list for the other
	// tests, and "nothing seeded" would otherwise be a claim about the
	// fixture rather than about the page.
	body := warmHome(t, func(f *fakeStore) {
		f.sampleList = nil
		f.dependencies = nil
	})

	if strings.Contains(body, `id="samples"`) {
		t.Error("an empty sample strip rendered")
	}
	if strings.Contains(body, `id="moved"`) {
		t.Error("an empty dependency strip rendered")
	}
}

// warmHome renders the landing page with the front-page strips already loaded.
func warmHome(t *testing.T, seed func(*fakeStore)) string {
	t.Helper()
	store := newFakeStore()
	seed(store)
	s := &site{
		d:    Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()},
		tmpl: parseTemplates(),
	}
	s.refreshHomeAssets(5 * time.Second)
	s.handAt = time.Now()

	req := httptest.NewRequest(http.MethodGet, "https://codesamplex.dev/en/", nil)
	rec := httptest.NewRecorder()
	s.landing(rec, req, "en")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	return rec.Body.String()
}

// The front page uses the same headline the collection does.
//
// The queue writes goals as "verify <symbol> in pkg:<eco>/<name>@<version>",
// and the coordinate is already on the line below. Printing the suffix says
// the same thing twice in the one place a reader scans first, and it is what
// overflowed the collection's cards before sampleGoalHeadline existed.
func TestTheFrontPageTrimsTheCoordinateOffAGoal(t *testing.T) {
	body := warmHome(t, func(f *fakeStore) {
		f.sampleList = []SampleListItem{{
			SampleID:  "sha256:bbb",
			Goal:      "verify go.etcd.io/bbolt.DB.Batch in pkg:golang/go.etcd.io/bbolt@v1.4.3",
			Ecosystem: "golang", Name: "go.etcd.io/bbolt", Version: "v1.4.3",
			Symbols: []string{"go.etcd.io/bbolt.DB.Batch"}, Status: "PUBLISHED",
		}}
	})
	if !strings.Contains(body, "verify go.etcd.io/bbolt.DB.Batch") {
		t.Error("the goal is missing from the sample strip")
	}
	if strings.Contains(body, "in pkg:golang/go.etcd.io/bbolt@v1.4.3") {
		t.Error("the goal still carries the coordinate its own row prints")
	}
}

// The network's own size is trust support, not the opening statement.
//
// The page opened with 151.9K observations, 6K samples and 2K packages. Those
// are facts about this network; a visitor is asking what it found out. They
// stay on the page -- they are what makes the findings above them worth
// believing -- and they sit with the evidence grid they belong to.
func TestTheCountersComeAfterWhatWasFound(t *testing.T) {
	body := warmHome(t, func(f *fakeStore) {
		f.sampleList = []SampleListItem{{
			SampleID: "sha256:ccc", Goal: "verify something",
			Ecosystem: "npm", Name: "zod", Version: "4.4.3", Status: "PUBLISHED",
		}}
	})
	tiles := strings.Index(body, `class="matrixtiles"`)
	if tiles < 0 {
		t.Fatal("the counters left the page entirely")
	}
	for _, before := range []string{`id="measured"`, `id="samples"`} {
		if i := strings.Index(body, before); i < 0 || i > tiles {
			t.Errorf("%s is at %d, after the counters at %d", before, i, tiles)
		}
	}
}
