package web

// The sitemap is the crawl contract in file form: an index at /sitemap.xml
// (the address robots.txt has always advertised) naming section shards under
// /sitemaps/, each shard a plain urlset. It is generated from the same store
// queries the pages render from and cached for sitemapTTL, so the corpus
// follows production data with nobody regenerating a file — the completion
// bar is "the indexable corpus follows automatically", not "a sitemap
// exists".
//
// Three sections, in the order a reader meets the site:
//
//	static-1.xml    the per-locale landing cluster and the collection pages
//	packages-1.xml  every package the records inventory can rank
//	samples-1.xml   every published sample, at its canonical address
//
// There is no findings shard: findings have no detail URLs — they are rows
// of /findings, which the static shard lists — and advertising a file of
// zero URLs would be noise, not coverage.
//
// lastmod is the page's own data change, never the generation clock: a
// sample's publication date (the artifact is immutable), a package's last
// snapshot materialization. Pages with no honest date (the landing, the
// collection pages) carry none. The shard's lastmod in the index is the
// newest entry it contains, so a rebuild that changed nothing does not ask
// every crawler to refetch every shard.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// sitemapTTL is the freshness window: a new package, sample or finding row
// appears in the served sitemap at most this long after the store starts
// returning it. It is also the whole request-time cost story — every request
// inside the window is served from memory and touches no database.
const sitemapTTL = 15 * time.Minute

// Shard budgets. The sitemap protocol caps one file at 50,000 URLs and
// 50MB; splitting well under both means a section that grows past a budget
// gets a second shard instead of a protocol violation nobody is watching
// for.
const (
	sitemapShardURLLimit  = 40_000
	sitemapShardByteLimit = 40 * 1024 * 1024
)

// sitemapSampleBound bounds the corpus read behind one rebuild, in the
// spirit of the R2C-58 rule that no public request may scan unboundedly.
// The factory target is 10,000 samples, so this is 5x headroom — and when
// the corpus reaches it, the health log says so (sample_bound_hit=true)
// instead of the oldest samples quietly falling off the map.
const sitemapSampleBound = 50_000

// sampleIDRe guards the sample ids that go into the sitemap. Ids are
// content addresses ("sha256:<hex>"), and the colon is a legal path
// character that every canonical URL on the site already carries — so the
// id is emitted verbatim rather than percent-escaped, which would
// advertise a URL that differs from the page's own rel=canonical. Anything
// that is not that shape is skipped instead of guessed at.
var sampleIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:_-]{0,127}$`)

// sitemapEntry is one <url> before rendering.
type sitemapEntry struct {
	loc     string // absolute URL
	lastmod string // "YYYY-MM-DD"; "" ⇒ omitted
	alts    []alternate
}

// sitemapShard is one rendered /sitemaps/<name> file.
type sitemapShard struct {
	name    string
	body    []byte
	lastmod string // newest entry lastmod; "" ⇒ omitted from the index
	urls    int
}

// sitemapHealth is the count ledger of one build. Its purpose is the day a
// Search Console "discovered URLs" number disagrees with production: each
// field names one distinct way the served set can differ from the stored
// corpus, so the divergence has a cause instead of a shrug.
type sitemapHealth struct {
	builtAt time.Time
	urls    int // total advertised across all shards
	shards  int
	static  int
	// packages advertised vs the store's ranked-package corpus; the gap is
	// unroutablePackages — ecosystems the router does not serve, which have
	// no canonical page and so are not indexable.
	packages           int
	packageCorpus      int
	unroutablePackages int
	// samples advertised vs the store's published corpus (bounded read);
	// the gap is malformed ids, skipped rather than escaped into a guess.
	samples            int
	sampleCorpus       int
	malformedSampleIDs int
	// sampleBoundHit: the corpus read came back exactly at its bound, so
	// the true corpus may be larger and the oldest samples may be missing.
	sampleBoundHit bool
	err            string // last build error; "" on a clean build
}

// sitemapSnapshot is one built sitemap: the index document plus every
// shard, served from memory until the TTL expires.
type sitemapSnapshot struct {
	base   string
	index  []byte
	shards map[string][]byte
	health sitemapHealth
}

// sitemapStaticEntries is the hand-known section: the landing cluster
// (one entry per locale, each carrying the full alternate cluster) and the
// collection pages. /stats and /adapters are permanent redirects and stay
// out of the map.
func sitemapStaticEntries(base string) []sitemapEntry {
	landingAlts := landingAlternates(base)
	var out []sitemapEntry
	for _, code := range i18n.Supported {
		loc := base + "/"
		if code != i18n.Default {
			loc = base + "/" + code + "/"
		}
		out = append(out, sitemapEntry{loc: loc, alts: landingAlts})
	}
	for _, p := range []string{"/records", "/findings", "/gaps", "/dependencies", "/features"} {
		out = append(out, sitemapEntry{loc: base + p})
	}
	return out
}

// buildSitemapSnapshot reads the corpus and renders every shard plus the
// index. A store error aborts the build with whatever was assembled, so the
// caller can prefer a complete stale snapshot over a fresh hole — a shard
// that silently lost its section would advertise mass removal.
func (s *site) buildSitemapSnapshot(ctx context.Context, base string) (*sitemapSnapshot, error) {
	snap := &sitemapSnapshot{base: base, shards: map[string][]byte{}}
	snap.health.builtAt = time.Now()

	staticEntries := sitemapStaticEntries(base)
	snap.health.static = len(staticEntries)

	var pkgEntries []sitemapEntry
	hits, total, err := s.d.Store.RecordPackages(ctx, RecordFilter{}, 0, 0)
	if err != nil {
		return snap, fmt.Errorf("sitemap: packages: %w", err)
	}
	snap.health.packageCorpus = total
	for _, h := range hits {
		// A package outside the router's ecosystems has no page; listing
		// it would advertise a 404.
		if !knownEcosystems[h.Ecosystem] {
			snap.health.unroutablePackages++
			continue
		}
		pkgEntries = append(pkgEntries, sitemapEntry{
			loc: base + "/" + url.PathEscape(h.Ecosystem) + "/" + escapePathSegments(h.Name),
			// When this package's evidence last changed — materialized on
			// the snapshot row, not invented at render time.
			lastmod: h.UpdatedAt,
		})
	}
	snap.health.packages = len(pkgEntries)

	// Every published sample, at the address it declares canonical: the
	// human-readable /npm/<name>/<version>/samples/<slug> where the sample
	// names a release this site routes, and the content address otherwise.
	// Advertising the digest URL of a page that canonicalizes elsewhere asks
	// a crawler to index one address and then tells it to index another.
	// Quarantined samples never come back from ListSamples, so a removal
	// leaves the map at the next rebuild by the same mechanism an arrival
	// enters it.
	var sampleEntries []sitemapEntry
	samples, err := s.d.Store.ListSamples(ctx, sitemapSampleBound)
	if err != nil {
		return snap, fmt.Errorf("sitemap: samples: %w", err)
	}
	snap.health.sampleCorpus = len(samples)
	snap.health.sampleBoundHit = len(samples) == sitemapSampleBound
	for _, sm := range samples {
		if !sampleIDRe.MatchString(sm.SampleID) {
			snap.health.malformedSampleIDs++
			continue
		}
		// lastmod is the publication date: a sample is immutable once
		// published (its id is the hash of its contents), so the date it
		// was created is the only honest value.
		sampleEntries = append(sampleEntries, sitemapEntry{
			loc:     base + sm.Href(),
			lastmod: datePart(sm.CreatedAt),
		})
	}
	snap.health.samples = len(sampleEntries)

	var all []sitemapShard
	for _, sec := range []struct {
		name    string
		entries []sitemapEntry
	}{
		{"static", staticEntries},
		{"packages", pkgEntries},
		{"samples", sampleEntries},
	} {
		all = append(all, shardSection(sec.name, sec.entries)...)
	}
	var idx strings.Builder
	idx.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	idx.WriteString(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, sh := range all {
		snap.shards[sh.name] = sh.body
		snap.health.urls += sh.urls
		idx.WriteString("  <sitemap>\n    <loc>" + xmlEscape(base+"/sitemaps/"+sh.name) + "</loc>\n")
		if sh.lastmod != "" {
			idx.WriteString("    <lastmod>" + xmlEscape(sh.lastmod) + "</lastmod>\n")
		}
		idx.WriteString("  </sitemap>\n")
	}
	idx.WriteString("</sitemapindex>\n")
	snap.index = []byte(idx.String())
	snap.health.shards = len(all)
	return snap, nil
}

// shardSection renders one section into as many shards as its budgets
// require: <name>-1.xml, <name>-2.xml, … A section with nothing to say
// produces no shard at all rather than an empty file.
func shardSection(name string, entries []sitemapEntry) []sitemapShard {
	const (
		head = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
			`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">` + "\n"
		tail = "</urlset>\n"
	)
	var out []sitemapShard
	var b strings.Builder
	urls, lastmod := 0, ""
	flush := func() {
		if urls == 0 {
			return
		}
		out = append(out, sitemapShard{
			name:    name + "-" + strconv.Itoa(len(out)+1) + ".xml",
			body:    []byte(head + b.String() + tail),
			lastmod: lastmod,
			urls:    urls,
		})
		b.Reset()
		urls, lastmod = 0, ""
	}
	for _, e := range entries {
		rendered := renderSitemapEntry(e)
		if urls > 0 && (urls >= sitemapShardURLLimit || b.Len()+len(rendered) > sitemapShardByteLimit) {
			flush()
		}
		b.WriteString(rendered)
		urls++
		// Dates are YYYY-MM-DD, so the newest one is the string maximum.
		if e.lastmod > lastmod {
			lastmod = e.lastmod
		}
	}
	flush()
	return out
}

func renderSitemapEntry(e sitemapEntry) string {
	var b strings.Builder
	b.WriteString("  <url>\n    <loc>" + xmlEscape(e.loc) + "</loc>\n")
	if e.lastmod != "" {
		b.WriteString("    <lastmod>" + xmlEscape(e.lastmod) + "</lastmod>\n")
	}
	for _, a := range e.alts {
		b.WriteString(`    <xhtml:link rel="alternate" hreflang="` + a.Lang + `" href="` + xmlEscape(a.URL) + `"/>` + "\n")
	}
	b.WriteString("  </url>\n")
	return b.String()
}

// sitemapSnapshotFor returns the cached snapshot, rebuilding when the TTL
// has passed (or the canonical origin changed, which only happens in dev
// where the origin is derived per request). On a rebuild error the previous
// snapshot keeps serving and the next request retries — stale and complete
// beats fresh and holed.
func (s *site) sitemapSnapshotFor(r *http.Request) *sitemapSnapshot {
	base := s.base(r)
	s.sitemapMu.Lock()
	defer s.sitemapMu.Unlock()
	if c := s.sitemapCache; c != nil && c.base == base && time.Since(s.sitemapAt) <= sitemapTTL {
		return c
	}
	snap, err := s.buildSitemapSnapshot(r.Context(), base)
	if err != nil {
		snap.health.err = err.Error()
		log.Printf("web: sitemap rebuild failed: %v", err)
		if c := s.sitemapCache; c != nil && c.base == base {
			return c
		}
		// Nothing better exists yet: serve the partial build but do not
		// cache it, so the next request rebuilds instead of pinning a hole
		// for a whole TTL.
		return snap
	}
	s.sitemapCache, s.sitemapAt = snap, time.Now()
	h := snap.health
	// One line per rebuild, both sides of every count: this is the log an
	// operator reads when Search Console's discovered-URL number disagrees
	// with production, to tell a routing gap from a malformed id from a
	// saturated bound from a stale cache.
	log.Printf("web: sitemap rebuilt urls=%d shards=%d static=%d packages=%d/%d samples=%d/%d unroutable_packages=%d malformed_sample_ids=%d sample_bound_hit=%v",
		h.urls, h.shards, h.static, h.packages, h.packageCorpus, h.samples, h.sampleCorpus,
		h.unroutablePackages, h.malformedSampleIDs, h.sampleBoundHit)
	return snap
}

// sitemapHealthHeaders exposes when the served sitemap was built and how
// many URLs it advertises. `curl -sI /sitemap.xml` against production is
// the whole staleness probe: a built-at older than the TTL plus a margin
// means rebuilds are failing, and the URL count is the number to hold
// against the corpus and against Search Console.
func sitemapHealthHeaders(w http.ResponseWriter, h sitemapHealth) {
	w.Header().Set("X-Sitemap-Built", h.builtAt.UTC().Format(time.RFC3339))
	w.Header().Set("X-Sitemap-Urls", strconv.Itoa(h.urls))
}

// sitemapIndex serves GET /sitemap.xml.
func (s *site) sitemapIndex(w http.ResponseWriter, r *http.Request) {
	snap := s.sitemapSnapshotFor(r)
	sitemapHealthHeaders(w, snap.health)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(snap.index)
}

// sitemapShardPage serves GET /sitemaps/{file}. A shard name the current
// snapshot does not contain is a 404: the index is rebuilt with the shards,
// so the only way here is an outdated bookmark or a guess.
func (s *site) sitemapShardPage(w http.ResponseWriter, r *http.Request) {
	snap := s.sitemapSnapshotFor(r)
	body, ok := snap.shards[r.PathValue("file")]
	if !ok {
		s.notFound(w, r, s.negotiate(w, r))
		return
	}
	sitemapHealthHeaders(w, snap.health)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	_, _ = w.Write(body)
}
