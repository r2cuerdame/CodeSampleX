package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// These tests hold the search surface of a sample page to the shape the
// Search Console export of 2026-08-27 said it was missing: 187 sample pages,
// 1,546 impressions, 0 clicks, and 157 of those pages ranking inside the
// first ten results. Everything checked here is what a person sees before
// they decide whether to click.

// realSampleID is a full content address. The shared fixture's
// "sha256:d1e2f3" is deliberately short, and a short id has no readable URL
// — the slug carries eight hex characters of the digest so it cannot
// collide, and an id that has none keeps the content-addressed URL. Tests
// about the readable URL therefore need a realistic id.
const realSampleID = "sha256:5a2468d2cc16044ff2aa29fc872676bee16b67cf982a892863eead1b08cfcdf5"

// machineGoalSample seeds the store with the shape most of the production
// corpus actually has: a goal the authoring worker wrote, a symbol list, a
// contract that ran, and a receipt that passed.
func machineGoalSample(t *testing.T, store *fakeStore) {
	t.Helper()
	manifest := `{
	  "schemaVersion": 1,
	  "case": {"schemaVersion":1,"caseId":"case:sha256:1234","kind":"HOW",
	    "goal":"verify pkg:npm/browserslist@4.28.7",
	    "contract":[
	      "browserslist resolves browser target queries such as chrome version inequalities and defaults",
	      "browserslist.parseConfig parses INI-like multi-environment configuration strings"],
	    "packages":["pkg:npm/browserslist@4.28.7"]},
	  "packages": ["pkg:npm/browserslist@4.28.7"],
	  "symbols": ["browserslist.parseConfig", "browserslist.coverage"],
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"x64",
	    "runtime":"node","runtimeVersion":"22","executionContext":"node"},
	  "license": "MIT-0",
	  "contractCommand": ["node", "test/contract.mjs"],
	  "verifierAdapter": "node-typescript@1"
	}`
	store.samples[realSampleID] = SampleMeta{
		SampleID: realSampleID, Status: "CROSS_PASS", License: "MIT-0",
		CreatedAt: "2026-08-26T14:52:28Z", ManifestJSON: manifest,
		Files: []string{"csx.json", "test/contract.mjs"},
	}
	store.receipts[realSampleID] = []string{`{
	  "schemaVersion": 1, "sampleId": "` + realSampleID + `",
	  "caseId": "case:sha256:1234", "environmentHash": "sha256:eeee",
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"x64",
	    "runtime":"node","runtimeVersion":"22","executionContext":"node"},
	  "stages": {"resolve":"PASS","compile":"PASS","contract":"PASS"},
	  "verifierAdapter": "node-typescript@1", "sandboxCapability": "CONTAINER_RUN",
	  "logsDigest": "sha256:ffff", "createdAt": "2026-08-26T14:52:31Z",
	  "peerId": "ed25519:c1973797be207ac4", "peerPubkey": "cHVi", "peerSignature": "c2ln"
	}`}
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID: realSampleID, Goal: "verify pkg:npm/browserslist@4.28.7",
		Status: "CROSS_PASS", License: "MIT-0", Context: "node 22",
		CreatedAt: "2026-08-26", Version: "4.28.7", Kind: "HOW",
		Symbols: []string{"browserslist.parseConfig", "browserslist.coverage"},
	})
	store.samplePackages[realSampleID] = []string{"pkg:npm/browserslist@4.28.7"}
}

// browserslistHref is where that sample's canonical URL lands.
func browserslistHref() string {
	return "/npm/browserslist/4.28.7/samples/" +
		sampleSlug(realSampleID, "parseConfig, coverage")
}

// A machine goal contributes nothing a person searches for. What the
// manifest states about the same sample — which release, which APIs — does,
// and it is the release the 0-click queries were actually about ("eslint
// 9.39.5", "nanoid 3.3.17").
func TestMachineGoalDoesNotReachTheTitle(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	body := get(t, mux, "/samples/"+realSampleID).Body.String()
	mustContain(t, body,
		"<title>browserslist 4.28.7: parseConfig, coverage — Verified sample | CodeSampleX</title>")
	// The purl is an internal identifier and it is not what anybody types.
	title := titleOf(body)
	if strings.Contains(title, "pkg:") || strings.Contains(title, "%40") {
		t.Errorf("title carries a package URL: %q", title)
	}
	// The author's own goal is still on the page, verbatim, under Case. It
	// is a fact about the sample; it is simply not the page's headline.
	mustContain(t, body, "verify pkg:npm/browserslist@4.28.7")
}

// The description answers the four things a searcher weighs before
// clicking: is it real code, for what release, was it actually run, and
// what did the run establish.
func TestSampleDescriptionLeadsWithWhatWasProven(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	body := get(t, mux, "/samples/"+realSampleID).Body.String()
	desc := descriptionOf(body)
	for _, want := range []string{
		"Verified sample",  // it ran, and a receipt says so
		"npm browserslist", // ecosystem and package, the way people search
		"4.28.7",           // the exact release
		"node 22",          // where the contract ran
		"The contract ran on",
		"browserslist resolves browser", // what it established
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("description %q missing %q", desc, want)
		}
	}
	if n := len([]rune(desc)); n > descriptionBudget {
		t.Errorf("description is %d characters, budget is %d: %q", n, descriptionBudget, desc)
	}
	// The same sentence is the page's own opening line, so a snippet the
	// crawler writes from the body cannot contradict the one it takes from
	// the description.
	mustContain(t, body, `<p class="lead">`+desc)
}

// "Verified" is a claim about an execution. A published sample nothing has
// run is source that arrived — the same rule levelBadge applies to the
// badge, applied to the words a search engine shows.
func TestUnrunSampleIsNotTitledVerified(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)
	store.receipts[realSampleID] = nil

	body := get(t, mux, "/samples/"+realSampleID).Body.String()
	title := titleOf(body)
	if strings.Contains(title, i18n.T("en", "serp.label_verified")) {
		t.Errorf("a sample with no passing receipt is titled verified: %q", title)
	}
	mustContain(t, title, i18n.T("en", "serp.label_source"))
	// And the description says what is missing rather than quoting the
	// contract lines as though they were results.
	desc := descriptionOf(body)
	mustContain(t, desc, "no contract run is recorded")
	if strings.Contains(desc, "browserslist resolves browser target queries") {
		t.Errorf("an unrun sample quotes its contract as a result: %q", desc)
	}
}

// The internal evidence vocabulary is precise and it is ours. It is not
// what a person scanning ten blue links is reading for, so it may not lead
// the title.
func TestTitleDoesNotLeadWithInternalEvidenceTerms(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	for _, path := range []string{"/samples/" + realSampleID, browserslistHref()} {
		title := titleOf(get(t, mux, path).Body.String())
		head := title
		if i := strings.Index(head, " — "); i > 0 {
			head = head[:i]
		}
		for _, banned := range []string{"CROSS_PASS", "L4_", "L5_", "sha256:", "PUBLISHED"} {
			if strings.Contains(head, banned) {
				t.Errorf("%s: title leads with %q: %q", path, banned, title)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The canonical contract.

// The readable URL is a real page, not a redirect: it renders the sample
// and names itself canonical.
func TestSemanticSampleURLServesTheSampleAndIsSelfCanonical(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	href := browserslistHref()
	rec := get(t, mux, href)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s status = %d, want 200", href, rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, realSampleID) // it really is that sample
	if got := canonicalOf(body); got != "https://codesamplex.dev"+href {
		t.Errorf("canonical = %q, want the page's own URL", got)
	}
	// The locale cluster belongs to the canonical page, so it is complete here.
	if n := strings.Count(body, `rel="alternate" hreflang=`); n != len(i18n.Supported)+1 {
		t.Errorf("hreflang links = %d, want %d locales plus x-default",
			n, len(i18n.Supported))
	}
	for _, lang := range i18n.Supported {
		want := `hreflang="` + lang + `" href="https://codesamplex.dev` + href
		mustContain(t, body, want)
	}
}

// The content address stays a working page — it is the sample's identity,
// it is what the API and the CLI hand out, and a redirect would put one in
// front of every one of those callers. What changes is which of the two
// addresses is indexed.
func TestDigestSampleURLKeepsServingAndPointsAtTheReadableOne(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	rec := get(t, mux, "/samples/"+realSampleID)
	if rec.Code != http.StatusOK {
		t.Fatalf("digest URL status = %d, want 200 (it is the permanent identity)", rec.Code)
	}
	body := rec.Body.String()
	if got := canonicalOf(body); got != "https://codesamplex.dev"+browserslistHref() {
		t.Errorf("canonical = %q, want the readable URL", got)
	}
	// A page that disavows itself must not also publish a locale cluster of
	// its own: hreflang describes the canonical page, and a crawler settles
	// the contradiction by discarding one half of it.
	if strings.Contains(body, `rel="alternate" hreflang=`) {
		t.Error("a cross-canonical page still advertises its own hreflang cluster")
	}
	// og:url follows the canonical, so a share links the indexed page.
	mustContain(t, body, `property="og:url" content="https://codesamplex.dev`+browserslistHref()+`"`)
}

// A sample that names no routable release has no readable URL, and it must
// then keep naming itself canonical rather than pointing at nothing.
func TestSampleWithoutARoutableReleaseStaysContentAddressed(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()
	if got := canonicalOf(body); got != "https://codesamplex.dev/samples/sha256:d1e2f3" {
		t.Errorf("canonical = %q, want the content-addressed URL", got)
	}
	mustContain(t, body, `rel="alternate" hreflang="ko"`)
}

// Every locale of the readable URL is self-canonical, the rule the rest of
// the site already follows.
func TestSemanticSampleURLIsSelfCanonicalPerLocale(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	href := browserslistHref()
	for _, lang := range i18n.Supported {
		body := get(t, mux, href+"?lang="+lang).Body.String()
		want := "https://codesamplex.dev" + href
		if lang != i18n.Default {
			want += "?lang=" + lang
		}
		if got := canonicalOf(body); got != want {
			t.Errorf("%s canonical = %q, want %q", lang, got, want)
		}
	}
}

// A localized digest URL points at the SAME locale of the readable URL. It
// used to be possible for nine translations to canonicalize onto one page;
// the cross-canonical has to preserve the locale for the same reason.
func TestCrossCanonicalKeepsTheLocale(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	body := get(t, mux, "/samples/"+realSampleID+"?lang=ko").Body.String()
	want := "https://codesamplex.dev" + browserslistHref() + "?lang=ko"
	if got := canonicalOf(body); got != want {
		t.Errorf("canonical = %q, want %q", got, want)
	}
}

// One address, one form. A trailing slash is a second URL for the same page.
func TestSemanticSampleURLHasOneTrailingSlashRule(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	rec := get(t, mux, browserslistHref()+"/")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != browserslistHref() {
		t.Fatalf("Location = %q, want the slashless form", loc)
	}
	// And the target is the end of the chain, not another hop.
	if next := get(t, mux, loc); next.Code != http.StatusOK {
		t.Errorf("redirect target status = %d, want 200 (no chain)", next.Code)
	}
}

// A slug that resolves to nothing is a 404, not somebody else's sample.
func TestUnknownSampleSlugIsNotFound(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	for _, path := range []string{
		"/npm/browserslist/4.28.7/samples/not-a-real-slug-00000000",
		"/npm/browserslist/9.9.9/samples/" + sampleSlug(realSampleID, "parseConfig, coverage"),
	} {
		if rec := get(t, mux, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

// The samples segment is a route, and a package really can have an API
// spelled the same way. One segment after the version is still a symbol.
func TestSamplesSegmentDoesNotSwallowASymbolPage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID: "sha256:" + strings.Repeat("b", 64),
		Goal:     "List the collected samples", Status: "PUBLISHED",
		License: "MIT-0", CreatedAt: "2026-08-10",
		Version: "1.12.0", Symbols: []string{"samples"},
	})
	store.samplePackages["sha256:"+strings.Repeat("b", 64)] = []string{"pkg:npm/axios@1.12.0"}

	rec := get(t, mux, "/npm/axios/1.12.0/samples")
	if rec.Code != http.StatusOK {
		t.Fatalf("symbol page status = %d, want 200 (one segment is a symbol)", rec.Code)
	}
	// It is the symbol page, not a sample page: the symbol page names the
	// coordinate in its heading and lists the samples under it.
	mustContain(t, rec.Body.String(), "axios 1.12.0")
}

// ---------------------------------------------------------------------------
// Crawl contract.

// The sitemap advertises the address each page declares canonical. Anything
// else asks a crawler to fetch one URL and then tells it to index another.
func TestSitemapAdvertisesTheCanonicalSampleURL(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	body := get(t, mux, "/sitemap.xml").Body.String()
	mustContain(t, body, "<loc>https://codesamplex.dev"+browserslistHref()+"</loc>")
	mustNotContain(t, body, "<loc>https://codesamplex.dev/samples/"+realSampleID+"</loc>")
}

// Every URL the sitemap names answers 200 and names ITSELF canonical, with
// no redirect in between. This is the whole crawl contract in one pass:
// canonical, redirect chains and sitemap agreement are one property, and
// checking them apart is how they drift.
func TestEverySitemapURLIsAFinalSelfCanonicalPage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	body := get(t, mux, "/sitemap.xml").Body.String()
	locs := regexp.MustCompile(`<loc>([^<]+)</loc>`).FindAllStringSubmatch(body, -1)
	if len(locs) == 0 {
		t.Fatal("sitemap has no <loc> entries")
	}
	for _, m := range locs {
		loc := m[1]
		path := strings.TrimPrefix(loc, "https://codesamplex.dev")
		rec := get(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s status = %d, want 200 with no redirect", path, rec.Code)
			continue
		}
		if got := canonicalOf(rec.Body.String()); got != loc {
			t.Errorf("%s declares canonical %q, but the sitemap advertises %q", path, got, loc)
		}
	}
}

// A page that links a sample at one address while the sample declares
// another canonical is telling a crawler to ignore the link it was given.
func TestInternalSampleLinksUseTheCanonicalURL(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	// The symbol page is where a sample naming an API is linked from; the
	// version page carries the ones no API claims.
	for _, path := range []string{
		"/npm/browserslist/4.28.7/browserslist.parseConfig",
		"/npm/axios/1.12.0/axios.post",
	} {
		rec := get(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		body := rec.Body.String()
		mustNotContain(t, body, `href="/samples/`+realSampleID+`"`)
		// The anchor text is the release and the API, not the purl the
		// authoring worker wrote and not a content address.
		mustNotContain(t, body, ">verify pkg:")
	}
	body := get(t, mux, "/npm/browserslist/4.28.7/browserslist.parseConfig").Body.String()
	mustContain(t, body, `href="`+browserslistHref()+`"`)
	mustContain(t, body, ">browserslist 4.28.7: parseConfig, coverage</a>")
}

// The article's url and the breadcrumb's last item are the canonical page,
// so the structured data cannot name a third address.
func TestStructuredDataNamesTheCanonicalURL(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	canonical := "https://codesamplex.dev" + browserslistHref()
	for _, path := range []string{"/samples/" + realSampleID, browserslistHref()} {
		body := get(t, mux, path).Body.String()
		mustContain(t, body, `"url":"`+canonical+`"`)
		mustContain(t, body, `"item":"`+canonical+`"`)
		// The release is a page of its own and it is the sample's parent in
		// the readable URL, so the trail names it.
		mustContain(t, body, `"item":"https://codesamplex.dev/npm/browserslist/4.28.7"`)
	}
}

// ---------------------------------------------------------------------------
// Package, release and API pages.
//
// These are the pages the 0-click queries were actually about: "nanoid npm",
// "eslint 9.39.5", "nanoid 3.3.17" — a package, or a package and an exact
// release. What each page's title and description promise has to be the
// coordinate the reader typed.

func TestExplorerTitlesNameTheCoordinateThatWasSearched(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	cases := []struct {
		path  string
		title string
	}{
		{"/npm/browserslist", "browserslist npm compatibility — CodeSampleX"},
		{"/npm/browserslist/4.28.7", "browserslist 4.28.7 compatibility — CodeSampleX"},
		{"/npm/browserslist/4.28.7/browserslist.parseConfig",
			"browserslist.parseConfig browserslist 4.28.7 compatibility — CodeSampleX"},
	}
	for _, tc := range cases {
		rec := get(t, mux, tc.path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", tc.path, rec.Code)
		}
		if got := titleOf(rec.Body.String()); got != tc.title {
			t.Errorf("%s title = %q, want %q", tc.path, got, tc.title)
		}
	}
}

// A description repeated word for word on every release of every package is
// one a search engine rewrites or ignores. This one names what was recorded
// against THIS release — and names nothing when nothing was.
func TestReleaseDescriptionNamesWhatWasRecordedThere(t *testing.T) {
	mux, store := newTestMux(t, nil)
	machineGoalSample(t, store)

	desc := descriptionOf(get(t, mux, "/npm/axios/1.12.0").Body.String())
	mustContain(t, desc, "axios@1.12.0")
	mustContain(t, desc, "Verified samples written against this release: 1.")

	// browserslist has a sample and no snapshot, so it may claim the sample
	// and must not invent an environment row.
	bare := descriptionOf(get(t, mux, "/npm/browserslist/4.28.7").Body.String())
	mustContain(t, bare, "Verified samples written against this release: 1.")
	mustNotContain(t, bare, "Recorded environments:")
}

func TestReleaseEvidenceFactsClaimsNothingItWasNotGiven(t *testing.T) {
	if got := releaseEvidenceFacts("en", nil, nil); got != "" {
		t.Errorf("empty evidence produced %q", got)
	}
	// "unknown" is the absence of a context, not a context.
	unknown := []matrixRow{{Context: "unknown"}, {Context: "node 22", NoEvidence: true}}
	if got := releaseEvidenceFacts("en", unknown, nil); got != "" {
		t.Errorf("unmeasured rows produced %q", got)
	}
	many := []matrixRow{
		{Context: "node 22"}, {Context: "node 20"}, {Context: "node 18"},
		{Context: "node 22"}, {Context: "bun 1.2"}, {Context: "deno 2.1"},
	}
	got := releaseEvidenceFacts("en", many, nil)
	// Deduplicated and bounded: a snippet, not an inventory.
	if strings.Count(got, "node 22") != 1 {
		t.Errorf("contexts not deduplicated: %q", got)
	}
	if strings.Contains(got, "deno 2.1") {
		t.Errorf("context list is unbounded: %q", got)
	}
}

// ---------------------------------------------------------------------------
// The pure functions, at the shapes the corpus actually holds.

func TestSampleSubjectFromRealCorpusShapes(t *testing.T) {
	cases := []struct {
		name    string
		pkg     string
		goal    string
		symbols []string
		want    string
	}{
		{
			name: "bare machine goal falls back to the manifest symbols",
			pkg:  "tslib",
			goal: "verify pkg:npm/tslib@2.8.1",
			// tslib's goal names no API at all; its manifest names three.
			symbols: []string{"__assign", "__rest", "__spreadArray"},
			want:    "__assign, __rest, __spreadArray",
		},
		{
			name: "machine goal with a symbol, no manifest symbols",
			pkg:  "github.com/jackc/pgx/v5",
			goal: "verify github.com/jackc/pgx/v5/pgconn.ParseConfig in pkg:golang/github.com/jackc/pgx/v5@v5.10.0",
			want: "pgconn.ParseConfig",
		},
		{
			name:    "a scoped npm package never reaches the subject as a purl escape",
			pkg:     "@babel/core",
			goal:    "verify @babel/core in pkg:npm/%40babel/core@7.27.4",
			symbols: []string{"@babel/core", "parseSync", "types.isImportExpression"},
			// The symbol that IS the package name says nothing beside the
			// package name the title already carries.
			want: "parseSync, types.isImportExpression",
		},
		{
			name:    "a hand-written goal is the subject, as written",
			pkg:     "lru-cache",
			goal:    "Handle silent behavioral changes in lru-cache across version boundaries",
			symbols: []string{"LRUCache", "LRUCache#set"},
			want:    "Handle silent behavioral changes in lru-cache across version boundaries",
		},
		{
			name: "a hand-written goal that quotes a purl loses only the purl",
			pkg:  "example.com/enc",
			goal: "Assert the encoder round-trips in pkg:golang/example.com/enc@v1.2.3",
			want: "Assert the encoder round-trips",
		},
		{
			name:    "backslash-qualified symbols shorten to the class",
			pkg:     "symfony/console",
			goal:    "verify pkg:composer/symfony/console@7.3.0",
			symbols: []string{`Symfony\Component\Console\Application`},
			want:    "Application",
		},
		{
			name: "nothing to say stays nothing",
			pkg:  "x",
			goal: "verify pkg:npm/x@1.0.0",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sampleSubject(tc.pkg, tc.goal, tc.symbols); got != tc.want {
				t.Errorf("sampleSubject() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The head of the title — the part that survives Google's truncation — has
// to hold the release and the subject.
func TestTitleKeepsTheReleaseInsideTheBudget(t *testing.T) {
	long := strings.Repeat("configuration resolution behaviour ", 6)
	copy := buildSerpCopy("en", serpInput{
		SampleID: realSampleID, Ecosystem: "npm", Name: "browserslist",
		Version: "4.28.7", Goal: long, Verified: true,
	})
	head := copy.Title
	if i := strings.Index(head, " — "); i > 0 {
		head = head[:i]
	}
	if n := len([]rune(head)); n > titleBudget+1 {
		t.Errorf("title head is %d characters, budget is %d: %q", n, titleBudget, head)
	}
	if !strings.HasPrefix(head, "browserslist 4.28.7: ") {
		t.Errorf("truncation ate the release: %q", head)
	}
	if !strings.HasSuffix(head, "…") {
		t.Errorf("a truncated title does not say it was truncated: %q", head)
	}
}

// A slug is a function of the sample and of nothing else. If it were a
// function of the release's contents, publishing a second sample could take
// an URL that is already indexed.
func TestSampleSlugIsStableAndUnique(t *testing.T) {
	a := sampleSlug("sha256:aaaaaaaabbbbbbbb", "parseConfig, coverage")
	b := sampleSlug("sha256:ccccccccdddddddd", "parseConfig, coverage")
	if a == b {
		t.Errorf("two samples with the same subject share a slug: %q", a)
	}
	if again := sampleSlug("sha256:aaaaaaaabbbbbbbb", "parseConfig, coverage"); again != a {
		t.Errorf("slug is not stable: %q then %q", a, again)
	}
	if !strings.HasSuffix(a, "-aaaaaaaa") {
		t.Errorf("slug does not carry the content address: %q", a)
	}
	// A subject with nothing typeable in it still produces a resolvable slug.
	if got := sampleSlug("sha256:aaaaaaaabbbbbbbb", "!!!"); got != "aaaaaaaa" {
		t.Errorf("empty subject slug = %q, want the digest alone", got)
	}
	// An id that is not a content address has no readable URL to offer.
	if got := sampleSlug("nope", "parseConfig"); got != "" {
		t.Errorf("non-digest id produced a slug: %q", got)
	}
	if n := len(sampleSlug(realSampleID, strings.Repeat("verylongsubject ", 20))); n > sampleSlugMaxLen+sampleDigestLen+1 {
		t.Errorf("slug is unbounded: %d characters", n)
	}
}

// semanticSampleHref emits a URL into rel=canonical and into the sitemap, so
// it may only emit one the router resolves back to the same coordinate.
func TestSemanticSampleHrefOnlyEmitsRoutableURLs(t *testing.T) {
	ok := semanticSampleHref("golang", "github.com/jackc/pgx/v5", "v5.10.0", "parseconfig-5a2468d2")
	if ok != "/golang/github.com/jackc/pgx/v5/v5.10.0/samples/parseconfig-5a2468d2" {
		t.Errorf("golang module href = %q", ok)
	}
	// "@" is a legal path character and pkgHref has emitted it unescaped
	// since scoped packages were routed, so the sample URL agrees with the
	// package URL a crawler already has.
	scoped := semanticSampleHref("npm", "@babel/core", "7.27.4", "parsesync-5a2468d2")
	if scoped != "/npm/@babel/core/7.27.4/samples/parsesync-5a2468d2" {
		t.Errorf("scoped npm href = %q", scoped)
	}
	for _, tc := range []struct{ eco, name, version, slug string }{
		{"", "axios", "1.0.0", "s-00000000"},
		{"npm", "axios", "", "s-00000000"},
		{"npm", "axios", "1.0.0", ""},
		{"nosuchecosystem", "axios", "1.0.0", "s-00000000"},
		// A "version" the router would not recognise as one would split the
		// path somewhere else and resolve to a different page.
		{"npm", "axios", "latest", "s-00000000"},
		// A bare major in the golang namespace is part of the import path.
		{"golang", "github.com/x/y", "v5", "s-00000000"},
	} {
		if got := semanticSampleHref(tc.eco, tc.name, tc.version, tc.slug); got != "" {
			t.Errorf("semanticSampleHref(%q,%q,%q,%q) = %q, want \"\"",
				tc.eco, tc.name, tc.version, tc.slug, got)
		}
	}
}

// A round trip through the router is the only proof that matters for a URL
// this code advertises.
func TestSemanticHrefRoundTripsThroughTheRouter(t *testing.T) {
	cases := []struct{ eco, name, version string }{
		{"npm", "browserslist", "4.28.7"},
		{"npm", "@babel/core", "7.27.4"},
		{"golang", "github.com/jackc/pgx/v5", "v5.10.0"},
		{"pypi", "typing_extensions", "4.15.0"},
		{"maven", "org.slf4j/slf4j-api", "2.0.17"},
		{"cargo", "serde", "1.0.229"},
	}
	for _, tc := range cases {
		href := semanticSampleHref(tc.eco, tc.name, tc.version, "subject-5a2468d2")
		if href == "" {
			t.Errorf("%s/%s@%s got no readable URL", tc.eco, tc.name, tc.version)
			continue
		}
		rest := strings.TrimPrefix(href, "/"+tc.eco+"/")
		name, version, tail, ok := splitPackageRest(tc.eco, rest)
		if !ok || name != tc.name || version != tc.version {
			t.Errorf("%s did not round trip: name=%q version=%q ok=%v", href, name, version, ok)
			continue
		}
		if len(tail) != 2 || tail[0] != sampleSegment || tail[1] != "subject-5a2468d2" {
			t.Errorf("%s tail = %v", href, tail)
		}
	}
}

func titleOf(body string) string {
	const open = "<title>"
	i := strings.Index(body, open)
	if i < 0 {
		return ""
	}
	rest := body[i+len(open):]
	if j := strings.Index(rest, "</title>"); j >= 0 {
		return rest[:j]
	}
	return ""
}

func descriptionOf(body string) string {
	const marker = `<meta name="description" content="`
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}
