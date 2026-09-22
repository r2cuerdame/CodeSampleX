package web

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var locRe = regexp.MustCompile(`<loc>([^<]+)</loc>`)

// sitemapShards fetches /sitemap.xml, requires it to be a sitemap INDEX
// whose entries all live under /sitemaps/, fetches every shard, and returns
// them by name. This is how a crawler consumes the map, so it is how the
// tests read it.
func sitemapShards(t *testing.T, mux *http.ServeMux) map[string]string {
	t.Helper()
	rec := get(t, mux, "/sitemap.xml")
	if rec.Code != http.StatusOK {
		t.Fatalf("/sitemap.xml status = %d", rec.Code)
	}
	idx := rec.Body.String()
	if !strings.Contains(idx, "<sitemapindex") {
		t.Fatalf("/sitemap.xml is not a sitemap index:\n%s", truncate(idx))
	}
	locs := locRe.FindAllStringSubmatch(idx, -1)
	if len(locs) == 0 {
		t.Fatal("sitemap index names no shards")
	}
	shards := map[string]string{}
	for _, m := range locs {
		path := strings.TrimPrefix(m[1], "https://codesamplex.dev")
		if !strings.HasPrefix(path, "/sitemaps/") {
			t.Fatalf("index entry %q is not under /sitemaps/", m[1])
		}
		sh := get(t, mux, path)
		if sh.Code != http.StatusOK {
			t.Fatalf("shard %s answers %d", path, sh.Code)
		}
		if got := sh.Header().Get("Content-Type"); !strings.Contains(got, "application/xml") {
			t.Errorf("shard %s Content-Type = %q", path, got)
		}
		shards[strings.TrimPrefix(path, "/sitemaps/")] = sh.Body.String()
	}
	return shards
}

// sitemapBody is every shard body concatenated: the full advertised URL
// set, however it happens to be sharded.
func sitemapBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	var all strings.Builder
	for _, body := range sitemapShards(t, mux) {
		all.WriteString(body)
	}
	return all.String()
}

// Every URL the sitemap advertises must answer 200. A sitemap that
// advertises a URL the server does not serve spends crawl budget on errors,
// and it is the kind of mistake that only shows up in Search Console weeks
// later — so it is checked here instead. robots.txt must also still point
// at the index: discovery and the index are one path.
func TestSitemapURLsResolve(t *testing.T) {
	mux, _ := newTestMux(t, nil)

	robots := get(t, mux, "/robots.txt").Body.String()
	mustContain(t, robots, "Sitemap: https://codesamplex.dev/sitemap.xml")

	locs := locRe.FindAllStringSubmatch(sitemapBody(t, mux), -1)
	if len(locs) == 0 {
		t.Fatal("sitemap shards have no <loc> entries")
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
	shards := sitemapShards(t, mux)

	body := shards["samples-1.xml"]
	if body == "" {
		t.Fatal("no samples-1.xml shard")
	}
	mustContain(t, body, "<loc>https://codesamplex.dev/samples/sha256:d1e2f3</loc>")
	// Publication date as lastmod: a sample is immutable once published.
	mustContain(t, body, "<lastmod>2026-08-01</lastmod>")
}

// Every package the records inventory ranks is a canonical page, and every
// one of them belongs in the map — the hot-100 window used to mean the
// 101st package existed, ranked, answered 200 and was advertised nowhere.
// lastmod is the day the package's evidence last changed, materialized on
// the snapshot row; a package the store cannot date carries no lastmod
// rather than a generation timestamp.
func TestSitemapListsEveryRecordPackage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.packages = append(store.packages, PackageHit{
		Ecosystem: "pypi", Name: "typing-extensions", UpdatedAt: "2026-08-20",
	})

	body := sitemapShards(t, mux)["packages-1.xml"]
	if body == "" {
		t.Fatal("no packages-1.xml shard")
	}
	for _, p := range store.packages {
		mustContain(t, body, "<loc>https://codesamplex.dev/"+p.Ecosystem+"/"+p.Name+"</loc>")
	}
	mustContain(t, body, "<lastmod>2026-08-20</lastmod>")
}

// A package whose ecosystem the router does not serve has no page; listing
// it would advertise a guaranteed 404 (deleted/non-canonical URLs are
// removed, not escaped into hope).
func TestSitemapSkipsUnroutableEcosystems(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.packages = append(store.packages, PackageHit{
		Ecosystem: "conda", Name: "numpy", UpdatedAt: "2026-08-20",
	})

	mustNotContain(t, sitemapBody(t, mux), "/conda/numpy")
}

// The factory target is 10,000 samples. Every one must remain discoverable;
// ListSamples is newest-first, so checking the last row catches a cap that
// quietly evicts the oldest samples. At this size everything still fits in
// one samples shard, and every shard must retain ample room under both
// protocol limits.
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

	shards := sitemapShards(t, mux)
	oldest := "https://codesamplex.dev/samples/" + store.sampleList[factoryTarget-1].SampleID
	mustContain(t, shards["samples-1.xml"], "<loc>"+oldest+"</loc>")

	total := 0
	for name, body := range shards {
		if got := strings.Count(body, "<loc>"); got >= protocolURLLimit {
			t.Errorf("shard %s URL count = %d, must stay below %d", name, got, protocolURLLimit)
		}
		if got := len(body); got >= protocolByteLimit {
			t.Errorf("shard %s size = %d bytes, must stay below %d", name, got, protocolByteLimit)
		}
		total += strings.Count(body, "<loc>https://codesamplex.dev/samples/")
	}
	if total != factoryTarget {
		t.Errorf("sample <loc> count = %d, want %d", total, factoryTarget)
	}
}

// A section that outgrows one file's budget splits into numbered shards
// automatically — the alternative is a protocol violation (50,000 URLs per
// file) that nothing surfaces until Google silently stops reading the file.
func TestSitemapSplitsASectionPastTheShardBudget(t *testing.T) {
	const over = sitemapShardURLLimit + 1
	mux, store := newTestMux(t, nil)
	store.sampleList = make([]SampleListItem, 0, over)
	for i := 0; i < over; i++ {
		store.sampleList = append(store.sampleList, SampleListItem{
			SampleID:  fmt.Sprintf("sha256:%064x", i),
			CreatedAt: "2026-08-17",
		})
	}

	shards := sitemapShards(t, mux)
	first, second := shards["samples-1.xml"], shards["samples-2.xml"]
	if first == "" || second == "" {
		t.Fatalf("want samples-1.xml and samples-2.xml, got shards %v", shardNames(shards))
	}
	if got := strings.Count(first, "<loc>"); got != sitemapShardURLLimit {
		t.Errorf("samples-1.xml URL count = %d, want %d", got, sitemapShardURLLimit)
	}
	if got := strings.Count(second, "<loc>"); got != 1 {
		t.Errorf("samples-2.xml URL count = %d, want 1", got)
	}
}

func shardNames(shards map[string]string) []string {
	names := make([]string, 0, len(shards))
	for name := range shards {
		names = append(names, name)
	}
	return names
}

// TestSitemapSkipsMalformedSampleIDs: a sitemap entry that does not
// resolve is worse than a missing one, so anything that is not a content
// address is left out rather than escaped into a guess.
func TestSitemapSkipsMalformedSampleIDs(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.sampleList = append(store.sampleList, SampleListItem{
		SampleID: "sha256:bad id/with spaces?", Goal: "junk",
	})
	if strings.Contains(sitemapBody(t, mux), "with spaces") {
		t.Error("sitemap advertised a sample id that is not a content address")
	}
}

// The corpus-freshness contract: a new sample or package appears in the
// sitemap because the store returns it, with nobody editing or deploying a
// file — and a sample the store stops returning (quarantine, deletion)
// leaves the same way. Within one freshness window the map is served from
// the cache, so the arrival is visible from the next rebuild on; a second
// site here is what a rebuilt cache looks like.
func TestSitemapFollowsTheCorpusWithoutManualEdits(t *testing.T) {
	store := newFakeStore()
	d := Deps{Store: store, PublicURL: "https://codesamplex.dev", Build: testBuild()}
	muxBefore := http.NewServeMux()
	Register(muxBefore, d)
	before := sitemapBody(t, muxBefore)
	mustContain(t, before, "/samples/sha256:d1e2f3")
	mustNotContain(t, before, "sha256:"+strings.Repeat("e", 64))

	// The corpus moves: one sample arrives, one is quarantined, a package
	// gains evidence. No file is edited.
	store.sampleList = []SampleListItem{{
		SampleID: "sha256:" + strings.Repeat("e", 64), CreatedAt: "2026-08-28",
	}}
	store.packages = append(store.packages, PackageHit{
		Ecosystem: "cargo", Name: "serde", UpdatedAt: "2026-08-28",
	})

	// Same window, same cache: the served map may not have moved yet.
	mustNotContain(t, sitemapBody(t, muxBefore), strings.Repeat("e", 64))

	muxAfter := http.NewServeMux()
	Register(muxAfter, d)
	after := sitemapBody(t, muxAfter)
	mustContain(t, after, "/samples/sha256:"+strings.Repeat("e", 64))
	mustContain(t, after, "/cargo/serde")
	mustNotContain(t, after, "/samples/sha256:d1e2f3")
}

// Inside the freshness window every sitemap request — index and shards —
// serves from memory. The corpus read behind a rebuild is the expensive
// part, and it may run once per window, not once per crawler.
func TestSitemapServesFromCacheWithinTheWindow(t *testing.T) {
	mux, store := newTestMux(t, nil)
	sitemapBody(t, mux)
	sitemapBody(t, mux)
	get(t, mux, "/sitemap.xml")
	if store.listSamplesCalls != 1 {
		t.Errorf("ListSamples calls = %d, want 1 (rebuild once per window)", store.listSamplesCalls)
	}
}

// The health surface: X-Sitemap-Built says when the served snapshot was
// assembled (staleness is its age against the documented window), and
// X-Sitemap-Urls is the advertised count to hold against the corpus and
// against Search Console's discovered number.
func TestSitemapHealthHeaders(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/sitemap.xml")

	built := rec.Header().Get("X-Sitemap-Built")
	if _, err := time.Parse(time.RFC3339, built); err != nil {
		t.Fatalf("X-Sitemap-Built = %q: %v", built, err)
	}
	urls, err := strconv.Atoi(rec.Header().Get("X-Sitemap-Urls"))
	if err != nil {
		t.Fatalf("X-Sitemap-Urls = %q: %v", rec.Header().Get("X-Sitemap-Urls"), err)
	}
	if got := strings.Count(sitemapBody(t, mux), "<loc>"); got != urls {
		t.Errorf("X-Sitemap-Urls = %d, but shards advertise %d URLs", urls, got)
	}
}

// A shard the index does not name is a 404, not an empty 200 a crawler
// would keep fetching forever.
func TestUnknownSitemapShardIs404(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	if rec := get(t, mux, "/sitemaps/findings-9.xml"); rec.Code != http.StatusNotFound {
		t.Errorf("/sitemaps/findings-9.xml status = %d, want 404", rec.Code)
	}
}

// /samples is a primary collection hub and must be advertised in the static
// shard alongside /compatibility, /findings, /gaps, /dependencies and /features,
// with full hreflang alternate clusters.
func TestStaticSitemapIncludesSamplesAndHreflangAlternates(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	shards := sitemapShards(t, mux)
	static := shards["static-1.xml"]
	if static == "" {
		t.Fatal("no static-1.xml shard")
	}

	mustContain(t, static, "<loc>https://codesamplex.dev/samples</loc>")
	for _, p := range []string{"/compatibility", "/findings", "/samples", "/gaps", "/dependencies", "/features"} {
		mustContain(t, static, "<loc>https://codesamplex.dev"+p+"</loc>")
		mustContain(t, static, `xhtml:link rel="alternate" hreflang="ko" href="https://codesamplex.dev`+p+`?lang=ko"`)
		mustContain(t, static, `xhtml:link rel="alternate" hreflang="x-default" href="https://codesamplex.dev`+p+`"`)
	}
}

// Release pages were the one route family the map never advertised, even
// though three of the five converting queries in the 2026-09-05 opportunity
// map (#192) were release lookups ("eslint 9.39.5", "axios 1.19.0"). The
// gate was cost: the only per-release store read is per-package, an N+1 on
// every rebuild. The shard is built instead from the sample read the map
// already makes -- a release page renders 200 whenever it has one published
// sample (versionPage), so every (ecosystem, name, version) a listed sample
// names is a routable page, at zero extra store cost.
func TestSitemapListsReleasePagesNamedBySamples(t *testing.T) {
	mux, store := newTestMux(t, nil)
	full := func(c byte) string { return "sha256:" + strings.Repeat(string(c), 64) }
	store.sampleList = append(store.sampleList,
		// Two samples on one release: one entry, dated by the newer sample.
		SampleListItem{SampleID: full('a'), Goal: "one", Version: "1.12.0", CreatedAt: "2026-09-01"},
		SampleListItem{SampleID: full('b'), Goal: "two", Version: "1.12.0", CreatedAt: "2026-09-10"},
		// A release with no snapshot and no symbols: samples alone make its
		// page render, so it belongs in the map.
		SampleListItem{SampleID: full('c'), Goal: "pad", Version: "1.3.0", CreatedAt: "2026-09-03"},
		// A Go release spelled without its "v" in the manifest: the store
		// repairs the spelling (ParsePURL), so the map advertises the
		// canonical address and never the bare one, which is a 301.
		SampleListItem{SampleID: full('d'), Goal: "bare", Version: "1.2.0", CreatedAt: "2026-09-04"},
		// A sample that names no release keeps its content address and
		// contributes no release page.
		SampleListItem{SampleID: full('e'), Goal: "stdlib", CreatedAt: "2026-09-05"},
	)
	store.samplePackages[full('a')] = []string{"pkg:npm/axios@1.12.0"}
	store.samplePackages[full('b')] = []string{"pkg:npm/axios@1.12.0"}
	store.samplePackages[full('c')] = []string{"pkg:npm/left-pad@1.3.0"}
	store.samplePackages[full('d')] = []string{"pkg:golang/github.com/a/b@1.2.0"}

	shards := sitemapShards(t, mux)
	body := shards["releases-1.xml"]
	if body == "" {
		t.Fatalf("no releases-1.xml shard; shards: %v", shardNames(shards))
	}
	mustContain(t, body, "<loc>https://codesamplex.dev/npm/axios/1.12.0</loc>\n    <lastmod>2026-09-10</lastmod>")
	if n := strings.Count(body, "<loc>https://codesamplex.dev/npm/axios/1.12.0</loc>"); n != 1 {
		t.Errorf("axios 1.12.0 advertised %d times, want once", n)
	}
	mustContain(t, body, "<loc>https://codesamplex.dev/npm/left-pad/1.3.0</loc>")
	mustNotContain(t, body, "/golang/github.com/a/b/1.2.0</loc>")
	mustContain(t, body, "<loc>https://codesamplex.dev/golang/github.com/a/b/v1.2.0</loc>")
	// Release pages are the shard's whole content: no sample or package
	// URL leaks into it, and the sample shard is unchanged.
	mustNotContain(t, body, "/samples/")
	mustContain(t, shards["samples-1.xml"], "/npm/left-pad/1.3.0/samples/")

	// Every advertised release must answer 200 -- including the one that
	// has nothing but samples.
	for _, m := range locRe.FindAllStringSubmatch(body, -1) {
		path := strings.TrimPrefix(m[1], "https://codesamplex.dev")
		if rec := get(t, mux, path); rec.Code != http.StatusOK {
			t.Errorf("releases shard advertises %s, which answers %d", path, rec.Code)
		}
	}
	// The health headers count the new section too.
	idx := get(t, mux, "/sitemap.xml")
	urls, _ := strconv.Atoi(idx.Header().Get("X-Sitemap-Urls"))
	if want := strings.Count(sitemapBody(t, mux), "<url>"); urls != want {
		t.Errorf("X-Sitemap-Urls = %d, shards hold %d", urls, want)
	}
}

