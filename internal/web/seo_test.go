package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestSitemapURLsResolve walks every <loc> in the sitemap and fetches it.
// A sitemap that advertises a URL the server does not serve spends crawl
// budget on errors, and it is the kind of mistake that only shows up in
// Search Console weeks later — so it is checked here instead.
func TestSitemapURLsResolve(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/sitemap.xml").Body.String()
	locs := regexp.MustCompile(`<loc>([^<]+)</loc>`).FindAllStringSubmatch(body, -1)
	if len(locs) == 0 {
		t.Fatal("sitemap has no <loc> entries")
	}
	for _, m := range locs {
		path := strings.TrimPrefix(m[1], "https://codesamplex.dev")
		if rec := get(t, mux, path); rec.Code != http.StatusOK {
			t.Errorf("sitemap advertises %s, which answers %d", path, rec.Code)
		}
	}
}

// TestSitemapListsSamplePages pins the fix for the discoverability hole
// that made every published sample unreachable: the sitemap listed the
// landing cluster, /records and the hot packages, and nothing else. A
// sample page is the only page on this site that answers one specific
// question in words, so it is the one that has to be listed.
func TestSitemapListsSamplePages(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/sitemap.xml").Body.String()

	mustContain(t, body, "<loc>https://codesamplex.dev/samples/sha256:d1e2f3</loc>")
	// Publication date as lastmod: a sample is immutable once published.
	mustContain(t, body, "<lastmod>2026-08-01</lastmod>")
}

// The factory target is 10,000 samples. Every one must remain discoverable;
// ListSamples is newest-first, so checking the last row catches a cap that
// quietly evicts the oldest samples. The resulting single sitemap must also
// retain ample room under both protocol limits.
func TestSitemapCoversFactoryTargetWithinProtocolBudget(t *testing.T) {
	const (
		factoryTarget     = 10_000
		protocolURLLimit  = 50_000
		protocolByteLimit = 50 * 1024 * 1024
	)

	mux, store := newTestMux(t, nil)
	store.sampleList = make([]SampleListItem, 0, factoryTarget)
	for i := 0; i < factoryTarget; i++ {
		store.sampleList = append(store.sampleList, SampleListItem{
			SampleID:  fmt.Sprintf("sha256:%064x", i),
			CreatedAt: "2026-08-17",
		})
	}

	body := get(t, mux, "/sitemap.xml").Body.String()
	oldest := "https://codesamplex.dev/samples/" + store.sampleList[factoryTarget-1].SampleID
	mustContain(t, body, "<loc>"+oldest+"</loc>")

	if got := strings.Count(body, "<loc>https://codesamplex.dev/samples/"); got != factoryTarget {
		t.Errorf("sample <loc> count = %d, want %d", got, factoryTarget)
	}
	if got := strings.Count(body, "<loc>"); got >= protocolURLLimit {
		t.Errorf("sitemap URL count = %d, must stay below %d", got, protocolURLLimit)
	}
	if got := len(body); got >= protocolByteLimit {
		t.Errorf("sitemap size = %d bytes, must stay below %d", got, protocolByteLimit)
	}
}

// TestSitemapSkipsMalformedSampleIDs: a sitemap entry that does not
// resolve is worse than a missing one, so anything that is not a content
// address is left out rather than escaped into a guess.
func TestSitemapSkipsMalformedSampleIDs(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID: "sha256:bad id/with spaces?", Goal: "junk",
	})
	body := get(t, mux, "/sitemap.xml").Body.String()
	if strings.Contains(body, "with spaces") {
		t.Error("sitemap advertised a sample id that is not a content address")
	}
}

// TestSamplePageIsSearchable checks the head of a sample page: the title
// used to be "Sample sha256:d1e2f3… — CodeSampleX", a string nobody types
// into a search engine. The words a reader searches for are in the goal.
func TestSamplePageIsSearchable(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()

	mustContain(t, body,
		"<title>POST JSON with axios and retries · axios 1.12.0 — CodeSampleX</title>")
	// Description: the goal plus the facts that decide whether this answer
	// applies — the packages and the environment it ran in.
	mustContain(t, body, `content="POST JSON with axios and retries — npm/axios@1.12.0 · node 22.18"`)
	mustContain(t, body, `rel="canonical" href="https://codesamplex.dev/samples/sha256:d1e2f3"`)

	// Structured data: the article shape plus a trail back to the package.
	mustContain(t, body, `"@type":"TechArticle"`)
	mustContain(t, body, `"@type":"SoftwareSourceCode"`)
	mustContain(t, body, `"BreadcrumbList"`)
	mustContain(t, body, `"runtimePlatform":"node 22.18"`)
	mustContain(t, body, `"license":"https://spdx.org/licenses/MIT-0.html"`)
	mustContain(t, body, `property="og:type" content="article"`)

	// The heading is the question, and the packages link back to the
	// explorer so the page is not a dead end in either direction. The
	// target is the package page: a version page only exists when that
	// exact version string has a snapshot.
	mustContain(t, body, "<h1>POST JSON with axios and retries</h1>")
	mustContain(t, body, `href="/npm/axios"`)
}

// TestPackagePageExistsForASampledPackage: the sample page links to the
// package page, so that page has to exist even when the network has no
// snapshot for the package yet. Measured on the live site before this
// change: /cargo/serde answered 404 while a published sample named
// pkg:cargo/serde@1.0.229.
func TestPackagePageExistsForASampledPackage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID: "sha256:cafe01", Goal: "Deserialize with serde and reject unknown fields",
		Status: "PUBLISHED", License: "MIT-0", CreatedAt: "2026-08-02",
	})
	store.samplePackages["sha256:cafe01"] = []string{"pkg:cargo/serde@1.0.229"}

	rec := get(t, mux, "/cargo/serde")
	if rec.Code != http.StatusOK {
		t.Fatalf("/cargo/serde status = %d, want 200 (a sample names it)", rec.Code)
	}
	mustContain(t, rec.Body.String(), `href="/samples/sha256:cafe01"`)
}

// Verified samples cover eight ecosystems.  The package router originally
// kept the four-ecosystem observation allowlist and turned every Gem,
// Composer, Hex and pub link emitted by the sitemap into a 404.
func TestPackagePagesExistForEverySampleEcosystem(t *testing.T) {
	mux, store := newTestMux(t, nil)
	cases := []struct {
		ecosystem string
		name      string
		version   string
	}{
		{"gem", "rack-protection", "4.2.1"},
		{"composer", "guzzlehttp/guzzle", "8.0.2"},
		{"hex", "req", "0.7.2"},
		{"pub", "args", "2.7.0"},
	}
	for i, tc := range cases {
		id := fmt.Sprintf("sha256:ecosystem-%d", i)
		store.sampleList = append(store.sampleList, SampleListItem{
			SampleID: id, Goal: "exercise " + tc.name,
			Status: "PUBLISHED", License: "MIT-0", CreatedAt: "2026-08-17",
		})
		store.samplePackages[id] = []string{
			fmt.Sprintf("pkg:%s/%s@%s", tc.ecosystem, tc.name, tc.version),
		}
		path := "/" + tc.ecosystem + "/" + tc.name
		if rec := get(t, mux, path); rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, rec.Code)
		}
	}
}

// TestPackagePageLinksSamples: the sitemap gets a crawler to the package
// page, and this link is what gets it from there to the sample.
func TestPackagePageLinksSamples(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios").Body.String()
	mustContain(t, body, `href="/samples/sha256:d1e2f3"`)
	mustContain(t, body, "POST JSON with axios and retries")
	// A package with no samples must not sprout an empty section.
	other := get(t, mux, "/golang/github.com/a/b").Body.String()
	if strings.Contains(other, `href="/samples/`) {
		t.Error("package page listed a sample that does not name it")
	}
}

// TestOGTypeDefaultsToWebsite: only dated documents are articles.
func TestOGTypeDefaultsToWebsite(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range []string{"/", "/records", "/npm/axios"} {
		body := get(t, mux, path).Body.String()
		if !strings.Contains(body, `property="og:type" content="website"`) {
			t.Errorf("%s: og:type is not website", path)
		}
	}
}
