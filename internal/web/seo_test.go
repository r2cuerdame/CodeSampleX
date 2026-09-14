package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The landing names every measured ecosystem as a plain link into its
// filtered records inventory — and makes no capability claims there. The
// honest per-adapter matrix lives in docs/adapters.md and GET /v1/adapters,
// where a wrong nuance cannot mislead an installer.
func TestLandingLinksEcosystemsWithoutCapabilityClaims(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, lang := range i18n.Supported {
		path := "/" + lang + "/"
		if lang == i18n.Default {
			path = "/"
		}
		body := get(t, mux, path).Body.String()
		for _, eco := range landingEcosystems {
			href := `href="/compatibility?eco=` + eco
			if lang != i18n.Default {
				href = `href="/compatibility?eco=` + eco + `&amp;lang=` + lang
			}
			if !strings.Contains(body, href) {
				t.Errorf("%s: missing ecosystem record link for %q", path, eco)
			}
		}
		// No capability rows on the landing any more — claims live where
		// they can carry their caveats.
		for _, gone := range []string{
			i18n.T(lang, "support.maven_java_note"),
			`class="econote`,
		} {
			if strings.Contains(body, gone) {
				t.Errorf("%s: landing still carries capability copy %q", path, gone)
			}
		}
	}
}

// The sitemap tests live in sitemap_test.go, next to the index/shard/cache
// machinery they pin.

// TestSamplePageIsSearchable checks the head of a sample page.
//
// The title used to be "Sample sha256:d1e2f3… — CodeSampleX", a string nobody
// types into a search engine, so it was rebuilt from the goal. That was not
// enough: most goals in the corpus are the line the authoring worker prints
// for an agent to start from, so the live title became an internal purl
// printed twice. What answers the search is the release and the API, and
// whether a contract actually ran.
func TestSamplePageIsSearchable(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()

	mustContain(t, body,
		"<title>axios 1.12.0: POST JSON with axios and retries — Verified sample | CodeSampleX</title>")
	// Description: what it is, for which release, and — because a receipt
	// records it — where the contract ran and the first thing it established.
	mustContain(t, body,
		`content="Verified sample for npm axios 1.12.0: POST JSON with axios and retries.`)
	mustContain(t, body, `The contract ran on node 22.18 · linux/amd64 and passed:`)
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
	mustContain(t, body, "<h1>axios 1.12.0: POST JSON with axios and retries</h1>")
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
	// The package page says which version the answers were written
	// against, and that version has a page even with no snapshot evidence:
	// the samples are the reason to go there.
	mustContain(t, rec.Body.String(), `href="/cargo/serde/1.0.229"`)
	version := get(t, mux, "/cargo/serde/1.0.229")
	if version.Code != http.StatusOK {
		t.Fatalf("/cargo/serde/1.0.229 status = %d, want 200 (2 samples name it)", version.Code)
	}
	mustContain(t, version.Body.String(), `href="/samples/sha256:cafe01"`)
}

// Verified samples cover nine ecosystems. The package router originally
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
		{"maven", "org.apache.commons/commons-lang3", "3.17.0"},
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

// A sample answers one version of one API, so each page narrows: the
// package page counts the answers per version, the version page counts them
// per API, and the API's own page lists them. Dumping all of them at package
// level mixed versions into one pile — uuid alone has 96 published samples;
// dumping them at version level mixed APIs into one pile — pgx v5.10.0 alone
// printed 128 rows under a single heading.
func TestSamplesAreListedUnderTheirSymbol(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	pkg := get(t, mux, "/npm/axios").Body.String()
	mustContain(t, pkg, `href="/npm/axios/1.12.0"`)
	if strings.Contains(pkg, `href="/samples/sha256:d1e2f3"`) {
		t.Error("the package page still dumps individual samples")
	}

	version := get(t, mux, "/npm/axios/1.12.0").Body.String()
	if strings.Contains(version, `href="/samples/sha256:d1e2f3"`) {
		t.Error("the version page still dumps samples that belong to an API")
	}
	// What the version page keeps is the count, beside the API it belongs to.
	mustContain(t, version, `href="/npm/axios/1.12.0/axios.post`)
	mustContain(t, version, `class="symcount dim small">1</span>`)

	symbol := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()
	mustContain(t, symbol, `href="/samples/sha256:d1e2f3"`)
	mustContain(t, symbol, "POST JSON with axios and retries")
	// Categorised, not a flat pile: the row says which API and which kind.
	mustContain(t, symbol, `class="chip sym mono small">axios.post`)
	mustContain(t, symbol, `class="chip kind mono small">HOW`)

	// A version with no samples of its own must not borrow another's.
	if other := get(t, mux, "/npm/axios/1.11.0").Body.String(); strings.Contains(other, `href="/samples/`) {
		t.Error("a version page listed a sample written against another version")
	}
	// A package with no samples must not sprout an empty section.
	if other := get(t, mux, "/golang/github.com/a/b").Body.String(); strings.Contains(other, `href="/samples/`) {
		t.Error("package page listed a sample that does not name it")
	}
}

// TestOGTypeDefaultsToWebsite: only dated documents are articles.
func TestOGTypeDefaultsToWebsite(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	for _, path := range []string{"/", "/compatibility", "/npm/axios"} {
		body := get(t, mux, path).Body.String()
		if !strings.Contains(body, `property="og:type" content="website"`) {
			t.Errorf("%s: og:type is not website", path)
		}
	}
}

// The language picker is in the header, and its options are links.
//
// It used to be nine links along the bottom of every page, which a reader had
// to reach the end of the page to find — and the reader who needs it is the
// one who cannot read the page they are scrolling through.
//
// Links rather than a <select>, and this is the half worth pinning: a select
// needs script to navigate anywhere, and these anchors are how a crawler walks
// between the nine translations. The head's hreflang cluster says the same
// thing; losing one of the two would still be losing half the signal.
func TestTheLanguagePickerIsInTheHeaderAndKeepsItsLinks(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/").Body.String()

	head, _, ok := strings.Cut(body, "</nav>")
	if !ok {
		t.Fatal("no nav on the page")
	}
	if !strings.Contains(head, `class="langpick"`) {
		t.Error("the language picker is not in the primary navigation")
	}
	if strings.Contains(body, `class="langs"`) {
		t.Error("the old footer language row is still rendered")
	}
	// Every supported locale is reachable as an anchor.
	for _, code := range i18n.Supported {
		if !strings.Contains(head, `lang="`+code+`"`) {
			t.Errorf("locale %s has no link in the picker", code)
		}
	}
	if strings.Contains(head, "<select") {
		t.Error("the picker became a select; its options stop being links a crawler can follow")
	}
}

// /samples cards and JSON-LD must link directly to canonical semantic URLs
// (row.Href()) instead of content-addressed URLs (/samples/sha256:...),
// eliminating the GSC "Alternate page with proper canonical" bucket.
// Filtered/search queries must declare noindex.
func TestSamplesHubLinksToSemanticCanonicalURLs(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID:  "sha256:abcd1234ef",
		Goal:      "parse JSON with fastjson",
		Status:    "PUBLISHED",
		License:   "MIT",
		CreatedAt: "2026-09-01",
		Version:   "1.6.4",
		Symbols:   []string{"fastjson.Parser"},
		Ecosystem: "golang",
		Name:      "github.com/valyala/fastjson",
	})
	store.samplePackages["sha256:abcd1234ef"] = []string{"pkg:golang/github.com/valyala/fastjson@1.6.4"}

	body := get(t, mux, "/samples").Body.String()
	wantHref := store.sampleList[len(store.sampleList)-1].Href()
	if !strings.HasPrefix(wantHref, "/golang/github.com/valyala/fastjson/1.6.4/samples/") {
		t.Fatalf("row.Href() = %q, want semantic sample href", wantHref)
	}

	mustContain(t, body, `href="`+wantHref+`"`)
	mustNotContain(t, body, `href="/samples/sha256:abcd1234ef"`)
	mustContain(t, body, `https://codesamplex.dev`+wantHref)

	// Search query variants must emit noindex to prevent indexing thin/empty filter variants.
	searchBody := get(t, mux, "/samples?q=valyala").Body.String()
	mustContain(t, searchBody, `<meta name="robots" content="noindex, follow">`)
}

// Error pages (404, 503) must emit <meta name="robots" content="noindex, follow">
// to prevent transient errors or non-existent URLs from being queued in Search Console.
func TestErrorPagesRenderRobotsNoIndex(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	rec := get(t, mux, "/nonexistent-page-url")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /nonexistent-page-url status = %d, want 404", rec.Code)
	}
	mustContain(t, rec.Body.String(), `<meta name="robots" content="noindex, follow">`)
	mustNotContain(t, rec.Body.String(), `rel="canonical"`)
}

// A non-existent or 0-sample seeder must return 404 with noindex rather than
// returning 200 with an empty template (which GSC flags as a Soft 404).
func TestEmptySeederReturns404(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	rec := get(t, mux, "/seeders/nonexistent_user_with_no_samples")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET empty seeder = %d, want 404", rec.Code)
	}
	mustContain(t, rec.Body.String(), `<meta name="robots" content="noindex, follow">`)
}

type ldBreadcrumbItem struct {
	Type     string `json:"@type"`
	Position int    `json:"position"`
	Name     string `json:"name"`
	Item     string `json:"item"`
}

type ldBreadcrumbList struct {
	Type            string             `json:"@type"`
	ItemListElement []ldBreadcrumbItem `json:"itemListElement"`
}

func extractBreadcrumbList(t *testing.T, html string) ldBreadcrumbList {
	t.Helper()
	const startTag = `<script type="application/ld+json">`
	const endTag = `</script>`
	idx := 0
	for {
		start := strings.Index(html[idx:], startTag)
		if start < 0 {
			break
		}
		start += idx + len(startTag)
		end := strings.Index(html[start:], endTag)
		if end < 0 {
			break
		}
		jsonStr := html[start : start+end]
		if strings.Contains(jsonStr, `"BreadcrumbList"`) {
			var b ldBreadcrumbList
			if err := json.Unmarshal([]byte(jsonStr), &b); err != nil {
				t.Fatalf("unmarshal BreadcrumbList JSON-LD: %v\nJSON:\n%s", err, jsonStr)
			}
			return b
		}
		idx = start + end + len(endTag)
	}
	t.Fatalf("no BreadcrumbList JSON-LD found in HTML")
	return ldBreadcrumbList{}
}

// All four exploration tiers (Package, Version, Symbol, Sample) must share a
// strictly consistent BreadcrumbList JSON-LD hierarchy with the ecosystem
// step included:
// Package: Home -> Eco -> Pkg
// Version: Home -> Eco -> Pkg -> Ver
// Symbol:  Home -> Eco -> Pkg -> Ver -> Sym
// Sample:  Home -> Eco -> Pkg -> Ver -> Sample
func TestBreadcrumbListJSONLDConsistencyAcrossTiers(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	base := "https://codesamplex.dev"
	const (
		eco     = "npm"
		name    = "browserslist"
		version = "4.28.7"
		symbol  = "parseConfig"
	)

	// 1. Package page: Home -> Eco -> Pkg
	pkgCrumbs := extractBreadcrumbList(t, get(t, mux, "/"+eco+"/"+name).Body.String())
	if got := len(pkgCrumbs.ItemListElement); got != 3 {
		t.Fatalf("package breadcrumb length = %d, want 3", got)
	}
	if pkgCrumbs.ItemListElement[0].Name != "CodeSampleX" || pkgCrumbs.ItemListElement[0].Item != base+"/" {
		t.Errorf("package crumb 1 = %+v", pkgCrumbs.ItemListElement[0])
	}
	if pkgCrumbs.ItemListElement[1].Name != eco || pkgCrumbs.ItemListElement[1].Item != base+"/compatibility?eco="+eco {
		t.Errorf("package crumb 2 (ecosystem) = %+v", pkgCrumbs.ItemListElement[1])
	}
	if pkgCrumbs.ItemListElement[2].Name != name || pkgCrumbs.ItemListElement[2].Item != base+"/"+eco+"/"+name {
		t.Errorf("package crumb 3 = %+v", pkgCrumbs.ItemListElement[2])
	}

	// 2. Version page: Home -> Eco -> Pkg -> Ver
	verCrumbs := extractBreadcrumbList(t, get(t, mux, "/"+eco+"/"+name+"/"+version).Body.String())
	if got := len(verCrumbs.ItemListElement); got != 4 {
		t.Fatalf("version breadcrumb length = %d, want 4", got)
	}
	if verCrumbs.ItemListElement[1].Name != eco || verCrumbs.ItemListElement[1].Item != base+"/compatibility?eco="+eco {
		t.Errorf("version crumb 2 (ecosystem) = %+v", verCrumbs.ItemListElement[1])
	}
	if verCrumbs.ItemListElement[2].Name != name || verCrumbs.ItemListElement[2].Item != base+"/"+eco+"/"+name {
		t.Errorf("version crumb 3 = %+v", verCrumbs.ItemListElement[2])
	}
	if verCrumbs.ItemListElement[3].Name != version || verCrumbs.ItemListElement[3].Item != base+"/"+eco+"/"+name+"/"+version {
		t.Errorf("version crumb 4 = %+v", verCrumbs.ItemListElement[3])
	}

	// 3. Symbol page: Home -> Eco -> Pkg -> Ver -> Sym
	symCrumbs := extractBreadcrumbList(t, get(t, mux, "/"+eco+"/"+name+"/"+version+"/"+symbol).Body.String())
	if got := len(symCrumbs.ItemListElement); got != 5 {
		t.Fatalf("symbol breadcrumb length = %d, want 5", got)
	}
	if symCrumbs.ItemListElement[1].Name != eco || symCrumbs.ItemListElement[1].Item != base+"/compatibility?eco="+eco {
		t.Errorf("symbol crumb 2 (ecosystem) = %+v", symCrumbs.ItemListElement[1])
	}
	if symCrumbs.ItemListElement[2].Name != name || symCrumbs.ItemListElement[2].Item != base+"/"+eco+"/"+name {
		t.Errorf("symbol crumb 3 = %+v", symCrumbs.ItemListElement[2])
	}
	if symCrumbs.ItemListElement[3].Name != version || symCrumbs.ItemListElement[3].Item != base+"/"+eco+"/"+name+"/"+version {
		t.Errorf("symbol crumb 4 = %+v", symCrumbs.ItemListElement[3])
	}
	if symCrumbs.ItemListElement[4].Name != symbol || symCrumbs.ItemListElement[4].Item != base+"/"+eco+"/"+name+"/"+version+"/"+symbol {
		t.Errorf("symbol crumb 5 = %+v", symCrumbs.ItemListElement[4])
	}

	// 4. Sample page: Home -> Eco -> Pkg -> Ver -> Sample
	sampleURL := browserslistHref()
	sampleCrumbs := extractBreadcrumbList(t, get(t, mux, sampleURL).Body.String())
	if got := len(sampleCrumbs.ItemListElement); got != 5 {
		t.Fatalf("sample breadcrumb length = %d, want 5", got)
	}
	if sampleCrumbs.ItemListElement[1].Name != eco || sampleCrumbs.ItemListElement[1].Item != base+"/compatibility?eco="+eco {
		t.Errorf("sample crumb 2 (ecosystem) = %+v", sampleCrumbs.ItemListElement[1])
	}
	if sampleCrumbs.ItemListElement[2].Name != name || sampleCrumbs.ItemListElement[2].Item != base+"/"+eco+"/"+name {
		t.Errorf("sample crumb 3 = %+v", sampleCrumbs.ItemListElement[2])
	}
	if sampleCrumbs.ItemListElement[3].Name != version || sampleCrumbs.ItemListElement[3].Item != base+"/"+eco+"/"+name+"/"+version {
		t.Errorf("sample crumb 4 = %+v", sampleCrumbs.ItemListElement[3])
	}
	if sampleCrumbs.ItemListElement[4].Name == "" || sampleCrumbs.ItemListElement[4].Item != base+sampleURL {
		t.Errorf("sample crumb 5 = %+v", sampleCrumbs.ItemListElement[4])
	}
}
