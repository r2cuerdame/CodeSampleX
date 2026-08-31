package web

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

var ldBlock = regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`)

// jsonLDTypes returns the @type of every structured-data block on a page.
func jsonLDTypes(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, m := range ldBlock.FindAllStringSubmatch(body, -1) {
		var doc map[string]any
		if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
			t.Fatalf("unparseable JSON-LD: %v\n%s", err, m[1])
		}
		if s, ok := doc["@type"].(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func jsonLDOfType(t *testing.T, body, want string) map[string]any {
	t.Helper()
	for _, m := range ldBlock.FindAllStringSubmatch(body, -1) {
		var doc map[string]any
		if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
			continue
		}
		if s, _ := doc["@type"].(string); s == want {
			return doc
		}
	}
	return nil
}

// The four list pages carried no structured data at all, so a crawler had only
// the markup to tell a collection of findings from one long article.
func TestTheListPagesSayTheyAreCollections(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}
	for _, path := range []string{"/samples", "/records", "/findings", "/dependencies"} {
		body := get(t, mux, path).Body.String()
		types := jsonLDTypes(t, body)
		found := false
		for _, ty := range types {
			if ty == "CollectionPage" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no CollectionPage; types = %v", path, types)
		}
	}
}

// The ItemList must describe what the CANONICAL url serves.
//
// s.page drops every query parameter from the canonical, so a search or a
// fifth page canonicalises back to the bare path. An ItemList emitted there
// would list rows that url does not serve — structured data describing a
// different page than the one it is attached to, which is worse than none.
func TestAFilteredListPageClaimsNoItemList(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}
	for _, path := range []string{
		"/samples?q=axios",
		"/records?q=axios",
		"/dependencies?q=axios",
		"/samples?page=2",
	} {
		body := get(t, mux, path).Body.String()
		if doc := jsonLDOfType(t, body, "CollectionPage"); doc != nil {
			t.Errorf("%s emitted a CollectionPage; its canonical serves different rows", path)
		}
	}
}

// A release page is the measurements taken at one coordinate, and Dataset is
// the vocabulary for that. The distribution points at the endpoint serving the
// same evidence as JSON, so the claim can be checked rather than trusted.
func TestAReleasePageDescribesItselfAsADataset(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}

	// The path form, which is the canonical url for a release. The
	// ?f_version= form canonicalises back to /npm/axios, so a dataset there
	// would be attached to a page whose canonical describes something else.
	body := get(t, mux, "/npm/axios/1.12.0").Body.String()
	doc := jsonLDOfType(t, body, "Dataset")
	if doc == nil {
		t.Fatalf("no Dataset on a pinned release page; types = %v", jsonLDTypes(t, body))
	}
	if name, _ := doc["name"].(string); !strings.Contains(name, "axios@1.12.0") {
		t.Errorf("dataset name = %q, want it to name the coordinate", name)
	}
	dist, _ := doc["distribution"].([]any)
	if len(dist) == 0 {
		t.Fatal("the dataset offers no distribution, so nothing about it is checkable")
	}
	d0, _ := dist[0].(map[string]any)
	if u, _ := d0["contentUrl"].(string); !strings.Contains(u, "/v1/registry/packages/") {
		t.Errorf("distribution contentUrl = %q, want the public registry endpoint", u)
	}

	// No license is asserted. What may be done with this evidence has not been
	// decided (#75), and naming a licence the project has not chosen would be
	// a claim in machine-readable form that nobody made.
	if _, ok := doc["license"]; ok {
		t.Error("the dataset asserts a license the project has not decided")
	}
}

// Without a version there is no single coordinate to describe, and a dataset
// naming a package without one would describe something nothing measured.
func TestAnUnpinnedPackagePageClaimsNoDataset(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0", "1.11.0"}
	body := get(t, mux, "/npm/axios").Body.String()
	if doc := jsonLDOfType(t, body, "Dataset"); doc != nil {
		t.Error("a package page spanning releases claimed to be one dataset")
	}
}

// Every block on every page has to parse. A malformed one is not a smaller
// benefit — search engines discard the whole script, so one bad block loses
// the good ones beside it.
func TestEveryStructuredDataBlockParses(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}
	for _, path := range []string{
		"/", "/samples", "/records", "/findings", "/dependencies", "/features",
		"/npm/axios", "/npm/axios/1.12.0",
	} {
		body := get(t, mux, path).Body.String()
		for _, m := range ldBlock.FindAllStringSubmatch(body, -1) {
			var doc map[string]any
			if err := json.Unmarshal([]byte(m[1]), &doc); err != nil {
				t.Errorf("%s: unparseable JSON-LD: %v", path, err)
				continue
			}
			if doc["@context"] != "https://schema.org" {
				t.Errorf("%s: block has @context %v", path, doc["@context"])
			}
			if _, ok := doc["@type"].(string); !ok {
				t.Errorf("%s: block has no @type", path)
			}
		}
	}
}
