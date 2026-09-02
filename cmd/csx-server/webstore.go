package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/blob"
	"github.com/r2cuerdame/codesamplex/internal/web"
)

// webStore adapts serverstore.Store (+ the blob store) to the read-only
// consumer interface the website defines. Everything here serves snapshots
// and metadata — raw evidence is never aggregated per request (goal.md §14.5).
type webStore struct {
	s     serverstore.Store
	blobs blob.Store

	// Environment filters need materialized snapshot rows. Cache that
	// immutable read briefly and serialize refreshes so concurrent public
	// filter requests do not each materialize and parse the whole table on
	// the small production host.
	snapshotMu         sync.Mutex
	snapshotAt         time.Time
	snapshotRows       []serverstore.SnapshotRow
	snapshotRefreshing bool
	snapshotRetryAt    time.Time

	// The landing and sitemap rank packages from the materialized page
	// inventory. A process restart used to put even that full read back on the
	// first landing request, before the response wrote a single byte. Keep the
	// last complete ranking and refresh one copy in the background instead.
	hotMu         sync.Mutex
	hotAt         time.Time
	hotRows       []web.PackageHit
	hotRefreshing bool
	hotRetryAt    time.Time

	// The per-package evidence recency the record inventory orders by,
	// cached on the same terms and for the same reason.
	updatedMu         sync.Mutex
	updatedAtRead     time.Time
	updatedAt         map[string]time.Time
	updatedRefreshing bool
	updatedRetryAt    time.Time

	// The materialized (purl, symbol) page inventory, cached for the same
	// reason again. This one matters most: assembling a package's cube asks
	// for it once for the version list and once more per version.
	targetsMu         sync.Mutex
	targetsAt         time.Time
	targetsRows       []serverstore.SnapshotTarget
	targetsRefreshing bool
	targetsRetryAt    time.Time
}

// The records page reads the whole snapshot table to rank and filter it.
// On production that table is 17,255 rows but 149MB of jsonb serialised, and
// ListSnapshots takes 36s there -- all from cache, so two cores of CPU, not
// disk. Snapshots change only when the builder writes them, once per tick at
// most, so the cache is sized to that cadence and refreshed BEHIND a request
// the way HotPackages is: hand back what is cached at once, reload under a
// background-class context with its own budget, retry later on failure.
//
// This used to be a 30-second TTL refreshed synchronously under the mutex.
// Any visitor more than 30s after the last one paid the full reload and
// everyone behind them queued on the lock; during a builder pass the read
// hit its statement timeout and the page came back empty. Measured
// 2026-09-02: 7.5s cold, then 1.0s and 1.8s.
//
// The one difference from HotPackages is the cold start. A widget may render
// empty; the records page may not, so with nothing cached the first request
// still waits for the read on its own deadline.
const (
	recordSnapshotCacheTTL       = 5 * time.Minute
	recordSnapshotRefreshTimeout = 2 * time.Minute
	recordSnapshotRetryDelay     = 30 * time.Second
)

func backgroundRefreshCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(
		serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground),
		recordSnapshotRefreshTimeout,
	)
}

func (w *webStore) cachedSnapshots(ctx context.Context) ([]serverstore.SnapshotRow, error) {
	w.snapshotMu.Lock()
	if w.snapshotAt.IsZero() {
		// Cold: nothing to serve, so this request loads on its own clock.
		w.snapshotMu.Unlock()
		rows, err := w.s.ListSnapshots(ctx)
		if err != nil {
			return nil, err
		}
		w.snapshotMu.Lock()
		if w.snapshotAt.IsZero() {
			w.snapshotRows, w.snapshotAt = rows, time.Now()
		}
		rows = w.snapshotRows
		w.snapshotMu.Unlock()
		return rows, nil
	}
	now := time.Now()
	rows := w.snapshotRows
	if !w.snapshotAt.After(now.Add(-recordSnapshotCacheTTL)) &&
		!w.snapshotRefreshing && !now.Before(w.snapshotRetryAt) {
		w.snapshotRefreshing = true
		go w.refreshSnapshots()
	}
	w.snapshotMu.Unlock()
	return rows, nil
}

func (w *webStore) refreshSnapshots() {
	ctx, cancel := backgroundRefreshCtx()
	defer cancel()
	rows, err := w.s.ListSnapshots(ctx)
	w.snapshotMu.Lock()
	defer w.snapshotMu.Unlock()
	w.snapshotRefreshing = false
	if err != nil {
		w.snapshotRetryAt = time.Now().Add(recordSnapshotRetryDelay)
		return
	}
	w.snapshotRows, w.snapshotAt = rows, time.Now()
	w.snapshotRetryAt = time.Time{}
}

func (w *webStore) cachedSnapshotUpdatedAt(ctx context.Context) map[string]time.Time {
	w.updatedMu.Lock()
	if w.updatedAtRead.IsZero() {
		w.updatedMu.Unlock()
		updated, err := w.s.SnapshotUpdatedAt(ctx)
		w.updatedMu.Lock()
		defer w.updatedMu.Unlock()
		if err != nil {
			return w.updatedAt
		}
		if w.updatedAtRead.IsZero() {
			w.updatedAt, w.updatedAtRead = updated, time.Now()
		}
		return w.updatedAt
	}
	now := time.Now()
	updated := w.updatedAt
	if !w.updatedAtRead.After(now.Add(-recordSnapshotCacheTTL)) &&
		!w.updatedRefreshing && !now.Before(w.updatedRetryAt) {
		w.updatedRefreshing = true
		go w.refreshSnapshotUpdatedAt()
	}
	w.updatedMu.Unlock()
	return updated
}

func (w *webStore) refreshSnapshotUpdatedAt() {
	ctx, cancel := backgroundRefreshCtx()
	defer cancel()
	updated, err := w.s.SnapshotUpdatedAt(ctx)
	w.updatedMu.Lock()
	defer w.updatedMu.Unlock()
	w.updatedRefreshing = false
	if err != nil {
		w.updatedRetryAt = time.Now().Add(recordSnapshotRetryDelay)
		return
	}
	w.updatedAt, w.updatedAtRead = updated, time.Now()
	w.updatedRetryAt = time.Time{}
}

func (w *webStore) cachedSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	// Only a visitor waiting on a page shares the foreground cache lane.
	// QueryClassOf intentionally treats an unclassified context as background,
	// so daemon/hero work cannot acquire this mutex by omission and move its
	// stall onto the next interactive request.
	if serverstore.QueryClassOf(ctx) != serverstore.ClassInteractive {
		return w.s.SnapshotKeys(ctx)
	}
	w.targetsMu.Lock()
	if w.targetsAt.IsZero() {
		w.targetsMu.Unlock()
		rows, err := w.s.SnapshotKeys(ctx)
		if err != nil {
			return nil, err
		}
		w.targetsMu.Lock()
		if w.targetsAt.IsZero() {
			w.targetsRows, w.targetsAt = rows, time.Now()
		}
		rows = w.targetsRows
		w.targetsMu.Unlock()
		return rows, nil
	}
	now := time.Now()
	rows := w.targetsRows
	if !w.targetsAt.After(now.Add(-recordSnapshotCacheTTL)) &&
		!w.targetsRefreshing && !now.Before(w.targetsRetryAt) {
		w.targetsRefreshing = true
		go w.refreshSnapshotTargets()
	}
	w.targetsMu.Unlock()
	return rows, nil
}

func (w *webStore) refreshSnapshotTargets() {
	ctx, cancel := backgroundRefreshCtx()
	defer cancel()
	rows, err := w.s.SnapshotKeys(ctx)
	w.targetsMu.Lock()
	defer w.targetsMu.Unlock()
	w.targetsRefreshing = false
	if err != nil {
		w.targetsRetryAt = time.Now().Add(recordSnapshotRetryDelay)
		return
	}
	w.targetsRows, w.targetsAt = rows, time.Now()
	w.targetsRetryAt = time.Time{}
}

func (w *webStore) LatestStatsJSON(ctx context.Context) (string, bool) {
	js, ok, err := w.s.GetLatestStats(ctx)
	return js, err == nil && ok
}

func (w *webStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	js, ok, err := w.s.GetSnapshot(ctx, purl, symbol)
	return js, err == nil && ok
}

func (w *webStore) PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error) {
	rows, err := w.s.ListPackageVersions(ctx, ecosystem, name)
	if err != nil {
		return nil, err
	}
	// This list labels its first item "latest", so version precedence must
	// decide the order. last_seen is evidence recency, not release recency:
	// an old release observed today must not become newer than a later release.
	// The SQL string sort is also insufficient (it puts 7.0.3 above 14.0.1).
	sort.SliceStable(rows, func(i, j int) bool {
		return domain.CompareVersions(rows[i].Version, rows[j].Version) > 0
	})
	// Only versions that HAVE a page. The list came from the packages
	// table, which the publicness gate also writes to -- including purls
	// whose evidence batch was then refused -- while the version page 404s
	// unless that exact version has a snapshot target. So a package page
	// listed versions under a heading whose empty state reads "No versions
	// with evidence yet", and every one of those links was a 404.
	//
	// A link into a 404 is worse than a slow page. This read is shared with
	// PackageSymbols through cachedSnapshotTargets, so the whole cube assembly
	// pays for it once rather than once per version.
	targets, terr := w.cachedSnapshotTargets(ctx)
	if terr != nil {
		return nil, terr
	}
	hasPage := map[string]bool{}
	for _, t := range targets {
		if p, err := domain.ParsePURL(t.PURL); err == nil &&
			p.Ecosystem == ecosystem && p.Name == name {
			hasPage[p.Version] = true
		}
	}
	versions := make([]string, 0, len(rows))
	for _, r := range rows {
		// The same two conditions versionPage renders on: a symbol with
		// evidence, or a package-level snapshot. Anything else is a 404.
		if !hasPage[r.Version] {
			if _, ok := w.SnapshotJSON(ctx, r.PURL, ""); !ok {
				continue
			}
		}
		versions = append(versions, r.Version)
	}
	return versions, nil
}

// SymbolPackageSpread counts the packages of one ecosystem carrying evidence
// for each named symbol. It reads the same cached target list the symbol list
// itself is built from, so it costs no query.
func (w *webStore) SymbolPackageSpread(ctx context.Context, ecosystem string, symbols []string) (map[string]int, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	targets, err := w.cachedSnapshotTargets(ctx)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(symbols))
	for _, sym := range symbols {
		want[sym] = true
	}
	pkgs := map[string]map[string]bool{}
	for _, t := range targets {
		if t.Symbol == "" || !want[t.Symbol] {
			continue
		}
		p, err := domain.ParsePURL(t.PURL)
		if err != nil || p.Ecosystem != ecosystem {
			continue
		}
		if pkgs[t.Symbol] == nil {
			pkgs[t.Symbol] = map[string]bool{}
		}
		pkgs[t.Symbol][p.Name] = true
	}
	out := make(map[string]int, len(pkgs))
	for sym, names := range pkgs {
		out[sym] = len(names)
	}
	return out, nil
}

func (w *webStore) PackageSymbols(ctx context.Context, ecosystem, name, version string) ([]string, error) {
	targets, err := w.cachedSnapshotTargets(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range targets {
		if t.Symbol == "" || seen[t.Symbol] {
			continue
		}
		p, err := domain.ParsePURL(t.PURL)
		if err != nil || p.Ecosystem != ecosystem || p.Name != name || p.Version != version {
			continue
		}
		seen[t.Symbol] = true
		out = append(out, t.Symbol)
	}
	sort.Strings(out)
	return out, nil
}

func (w *webStore) SampleMeta(ctx context.Context, id string) (web.SampleMeta, bool) {
	row, ok, err := w.s.GetSample(ctx, id)
	// Quarantine hides a sample from every serving read. GetSample returns
	// the raw row so the operator commands still see it; this is a serving
	// read, so it has to check.
	if err != nil || !ok || row.Quarantined {
		return web.SampleMeta{}, false
	}
	return web.SampleMeta{
		SampleID:     row.SampleID,
		Status:       row.Status,
		License:      row.License,
		OriginSeeder: row.OriginSeeder,
		CreatedAt:    row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		ManifestJSON: row.ManifestJSON,
		Files:        w.artifactFiles(ctx, id),
	}, true
}

func (w *webStore) SampleManifest(ctx context.Context, id string) (string, bool) {
	row, ok, err := w.s.GetSample(ctx, id)
	if err != nil || !ok || row.Quarantined {
		return "", false
	}
	return row.ManifestJSON, true
}

// artifactFiles lists entry names from the sample artifact; best-effort —
// an unreadable artifact just renders without a file list.
func (w *webStore) artifactFiles(ctx context.Context, id string) []string {
	if w.blobs == nil {
		return nil
	}
	rc, err := w.blobs.Get(ctx, id)
	if err != nil {
		return nil
	}
	defer rc.Close()
	gz, err := gzip.NewReader(io.LimitReader(rc, 1<<20))
	if err != nil {
		return nil
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var files []string
	for len(files) < 500 {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Typeflag == tar.TypeReg {
			files = append(files, h.Name)
		}
	}
	sort.Strings(files)
	return files
}

func (w *webStore) SampleReceipts(ctx context.Context, id string) ([]string, error) {
	rows, err := w.s.ReceiptsForSample(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ReceiptJSON)
	}
	return out, nil
}

// seederSampleLimit bounds one seeder page.
const seederSampleLimit = 200

func (w *webStore) SeederSamples(ctx context.Context, login string) ([]web.SampleListItem, error) {
	rows, err := w.s.SamplesBySeeder(ctx, login, seederSampleLimit)
	if err != nil {
		return nil, err
	}
	out := make([]web.SampleListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleListItem(r))
	}
	return out, nil
}

// ListSamples feeds the sitemap. serverstore.ListSamples already excludes
// quarantined rows, which is what makes it safe to advertise these URLs.
// SamplesPage is one page of the browsable sample collection.
//
// The total is counted, not probed. This used to read one row past the page to
// learn whether a next page existed and return `offset + len(rows)`, which is
// right only on the last page -- and the template renders it as an
// authoritative "{{.From}}-{{.To}} / {{.Total}}", so page 1 of a 4,683-sample
// corpus told the reader there were 25. A number a reader uses to decide
// whether the collection is worth walking has to be a number somebody counted;
// Records and Findings both count theirs.
func (w *webStore) SamplesPage(ctx context.Context, offset, limit int) ([]web.SampleListItem, int, error) {
	if limit <= 0 {
		limit = 24
	}
	total, err := w.s.CountSamples(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := w.s.ListSamplesPage(ctx, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]web.SampleListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleListItem(r))
	}
	return out, total, nil
}

func (w *webStore) SearchSamples(ctx context.Context, query string, offset, limit int) ([]web.SampleListItem, int, error) {
	if limit <= 0 {
		limit = 24
	}
	rows, total, err := w.s.SearchSamplesPage(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	out := make([]web.SampleListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleListItem(r))
	}
	return out, total, nil
}

func (w *webStore) ListSamples(ctx context.Context, limit int) ([]web.SampleListItem, error) {
	rows, err := w.s.ListSamples(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]web.SampleListItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, sampleListItem(r))
	}
	return out, nil
}

// PackageSamples returns the samples a package page links to.
//
// The store filters with a SQL LIKE pattern, where "_" is a wildcard —
// so "typing_extensions" would also match "typing-extensions". The
// manifest is re-checked here for an exact "pkg:<eco>/<name>@" prefix so
// a package page never advertises a sample about a different package.
func (w *webStore) PackageSamples(ctx context.Context, ecosystem, name string, limit int) ([]web.SampleListItem, error) {
	// The CANONICAL prefix. PURL.String() escapes a leading "@" to "%40",
	// so a scoped npm package is stored as pkg:npm/%40scope/name@... and a
	// prefix built by concatenation matched none of them. Every scoped
	// package — @tanstack, @babel, @modelcontextprotocol, a large share of
	// npm — showed an empty sample list on its own page while its samples
	// sat published and unreachable.
	// An empty version still renders its "@", which is what makes this an
	// exact-name prefix rather than one "foo" shares with "foo-bar".
	prefix := domain.PURL{Ecosystem: ecosystem, Name: name}.String()
	rows, err := w.s.VerifiedSamplesForPackages(ctx, []string{prefix + "%"}, limit)
	if err != nil {
		return nil, err
	}
	var out []web.SampleListItem
	for _, r := range rows {
		if !manifestNamesPackage(r.ManifestJSON, prefix) {
			continue
		}
		out = append(out, sampleListItem(r))
	}
	return out, nil
}

// ReleaseSamples returns the samples of ONE release, which is what resolves
// a human-readable sample URL back to its content address.
//
// The pattern is the exact release purl rather than a package prefix, so the
// bound applies per release. It is still re-checked in Go: the store matches
// with SQL LIKE, where "_" is a wildcard, so "typing_extensions@1.0.0" would
// also match "typing-extensions@1.0.0" — and resolving a URL to a sample
// about a different package is worse than not resolving it.
//
// Verification is deliberately not required. A readable URL is addressing,
// not a claim, and the page states for itself whether a contract ran.
func (w *webStore) ReleaseSamples(ctx context.Context, ecosystem, name, version string, limit int) ([]web.SampleListItem, error) {
	if version == "" {
		return nil, nil
	}
	exact := domain.PURL{Ecosystem: ecosystem, Name: name, Version: version}.String()
	rows, err := w.s.SamplesForPackages(ctx, []string{exact}, limit)
	if err != nil {
		return nil, err
	}
	var out []web.SampleListItem
	for _, r := range rows {
		if !manifestNamesRelease(r.ManifestJSON, exact) {
			continue
		}
		out = append(out, sampleListItem(r))
	}
	return out, nil
}

// manifestNamesRelease is manifestNamesPackage for an exact release: the
// purl has to match, not merely start the same way, so "@1.12.0" cannot
// answer for "@1.12.01".
func manifestNamesRelease(manifestJSON, purl string) bool {
	m, ok := parseManifest(manifestJSON)
	if !ok {
		return false
	}
	for _, p := range m.Packages {
		if p == purl {
			return true
		}
	}
	return false
}

func (w *webStore) PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]web.PackageCodeCount, error) {
	prefix := domain.PURL{Ecosystem: ecosystem, Name: name}.String()
	rows, err := w.s.VerifiedSampleCodeCounts(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]web.PackageCodeCount, 0, len(rows))
	for _, row := range rows {
		p, err := domain.ParsePURL(row.PURL)
		if err != nil || p.Ecosystem != ecosystem || p.Name != name || p.Version == "" {
			continue
		}
		out = append(out, web.PackageCodeCount{
			Version: p.Version, Symbol: row.Symbol, Samples: row.Samples,
		})
	}
	return out, nil
}

// Dependencies adapts the parent-side view of the same edges.
func (w *webStore) Dependencies(ctx context.Context, ecosystem, name string) ([]web.DependencyEdge, error) {
	rows, err := w.s.Dependencies(ctx, ecosystem, name)
	if err != nil {
		return nil, err
	}
	out := make([]web.DependencyEdge, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.DependencyEdge{
			ParentName: r.ParentName, ParentVersion: r.ParentVersion,
			ChildName: r.ChildName, ChildVersion: r.ChildVersion,
			Projects: int64(r.Projects),
		})
	}
	return out, nil
}

// derivedFindingPage is how many belief-declaring samples one database read
// returns.
//
// It bounds a READ, not the answer. The number it replaced bounded the
// answer: DerivedFindings used to take the newest 2,000 verified samples and
// look for beliefs inside that slice, so a finding stayed public only while
// fewer than 2,000 verified samples had been published after it. Production
// crossed that line and the machine-derived group fell from 543 to 250 with
// nothing quarantined, nothing invalidated and no receipt withdrawn — 308
// findings measured as still eligible, aged out of a window. Raising the
// number would have bought time and nothing else.
//
// So the eligibility test moved into the store and this pages through what
// comes back. A page is a bounded query and a bounded parse; the loop below
// stops when the caller has enough or the corpus runs out, which are the only
// two things that should ever stop it.
const derivedFindingPage = 500

// DerivedFindings returns the published samples whose case says what was
// believed, newest first, up to limit.
//
// The store narrows to samples that declare a belief — the JSON the manifest
// itself carries, since no column mirrors it and adding one would put a
// second answer beside the artifact's own copy. What is left here is the
// judgement that cannot be made in SQL: whether a contract line reads as
// prose a stranger can learn from. The result is cached by the caller, so
// this runs on a timer, not on a request.
// DerivedFindings walks the whole belief subset, not a window of it.
//
// The scan is bounded per query — ListVerifiedBeliefSamples narrows to
// non-quarantined samples with a contract PASS and a stated belief, and this
// pages it derivedFindingPage rows at a time — but the result is every finding
// the corpus holds. R2C-133: a durable finding must not vanish because newer
// samples arrived, and any count ceiling here reintroduces exactly that.
func (w *webStore) DerivedFindings(ctx context.Context) ([]web.DerivedFinding, error) {
	var out []web.DerivedFinding
	var cursor serverstore.SampleCursor
	for {
		rows, err := w.s.ListVerifiedBeliefSamples(ctx, cursor, derivedFindingPage)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			return out, nil
		}
		for _, r := range rows {
			m, ok := parseManifest(r.ManifestJSON)
			if !ok || strings.TrimSpace(m.Case.Believed) == "" {
				continue
			}
			measured := firstContractLine(m.Case.Contract)
			if measured == "" {
				// A belief with nothing measured against it is an opinion,
				// and an opinion is what this page exists not to publish.
				continue
			}
			eco, subject := findingSubject(m.Case.Packages)
			if eco == "" {
				continue
			}
			out = append(out, web.DerivedFinding{
				Ecosystem:   eco,
				Subject:     subject,
				Believed:    strings.TrimSpace(m.Case.Believed),
				Measured:    measured,
				SampleID:    r.SampleID,
				OS:          web.RecordEnvironmentOS(m.Environment),
				Runtime:     web.RecordEnvironmentRuntime(m.Environment),
				Environment: web.RecordEnvironmentSummary(m.Environment),
			})
		}
		if len(rows) < derivedFindingPage {
			return out, nil
		}
		cursor = serverstore.CursorFor(rows[len(rows)-1])
	}
}

// firstContractLine picks the assertion that reads as the measurement.
//
// A contract is a list of asserted facts, and the first one is usually the
// reason the sample was written — later lines are the supporting checks
// ("exit code 0", "no warnings on stderr") that every sample repeats.
//
// But it must also be READABLE, because this one goes on a public page
// beside a sentence of English. Authors write contract lines both ways:
//
//	assert Deleting a missing field raises NameError, and the message
//	now uses single quotes rather than backticks          <- a sentence
//	assert.strictEqual(ms(604800000), '7d');              <- an expression
//
// Both are perfectly good contract lines and the second is useless as the
// second half of a finding: a reader who does not already know the library
// learns nothing from it, and "believed X, measured assert.strictEqual(…)"
// contradicts nothing on its face. So this walks the contract for a line
// that reads as prose and gives up rather than printing an expression.
func firstContractLine(contract []string) string {
	for _, c := range contract {
		if c = strings.TrimSpace(c); readsAsProse(c) {
			return c
		}
	}
	return ""
}

// readsAsProse reports whether a contract line is a sentence rather than a
// code expression.
//
// The question is not how long the line is. Counting tokens let
//
//	assert.strictEqual(ms(604800000, { long: true }), '7 days');
//
// onto the live page: seven tokens and six identifiers, and nothing a
// reader can learn from. What actually separates the two is whether there
// is a SENTENCE OUTSIDE THE BRACKETS. An expression puts everything it
// says inside them and leaves the call name behind; a sentence that
// happens to cite a call keeps saying things afterwards:
//
//	ms(604800000) returns "7d" rather than "1w", because the largest
//	unit it formats is a day                              <- kept
//	Stack::new(a, b).layer(base) wraps base with a first, so b runs
//	first on the way in                                   <- kept
//	expect(x).toBe(1)                                     <- dropped
//
// The cost of being wrong is not symmetric, which is why this errs toward
// dropping: a sentence wrongly rejected costs one entry on a page that
// grows by itself, and an expression wrongly accepted is published under a
// heading promising a measurement in plain language.
// It is not enough on its own, either. Stripping brackets still let a
// whole Ruby statement through —
//
//	begin; record.user.name; rescue NoMethodError => e; end
//
// nine identifiers, none of them inside a bracket. What that line has none
// of is FUNCTION WORDS. An English sentence describing a measurement
// cannot avoid them: something is the, than, because, rather, into,
// without, still. A code expression contains none, in any language, and
// that turns out to be the cleanest line between the two.
func readsAsProse(s string) bool {
	bare := outsideBrackets(s)
	// Two, not three. The list lost its most common members to the
	// keyword overlap, and real sentences off the live page carry exactly
	// two of what is left: "PSR-18 sendRequest returns 4xx and 5xx status
	// codes AS valid instances WITHOUT throwing". A code expression still
	// carries none.
	return countWords(bare) >= 6 && countFunctionWords(bare) >= 2
}

// functionWords is the closed class English uses to hold a sentence
// together. Deliberately small: every entry has to be a word that carries
// no domain meaning, so a line full of them is prose and a line with none
// is not a sentence about anything.
// Words that are ALSO operators or builtins in a mainstream language are
// deliberately absent — and, or, not, in, is, all, any, each. A Python or
// Ruby assertion is full of them:
//
//	assert not Path(zf, "sub").exists() and not Path(zf, "sub").is_dir()
//
// which reached the live page carrying four of them and nothing else. A
// word only helps here if finding it means somebody was writing English.
var functionWords = map[string]bool{
	"the": true, "a": true, "an": true, "but": true, "was": true, "were": true,
	"been": true, "its": true, "this": true, "that": true, "these": true,
	"those": true, "of": true, "to": true, "into": true, "on": true,
	"with": true, "without": true, "from": true, "by": true,
	"as": true, "than": true, "rather": true, "because": true, "so": true,
	"no": true, "when": true, "while": true, "before": true,
	"after": true, "still": true, "only": true, "even": true, "their": true,
	"they": true, "which": true, "what": true, "does": true,
	"both": true, "every": true,
	"more": true, "less": true, "same": true, "instead": true, "own": true,
	"never": true, "always": true, "already": true, "yet": true, "at": true,
}

func countFunctionWords(s string) int {
	n := 0
	for _, f := range strings.Fields(strings.ToLower(s)) {
		f = strings.Trim(f, ".,;:!?()[]{}\"'`")
		if functionWords[f] {
			n++
		}
	}
	return n
}

// outsideBrackets removes every (), [] and {} span, including nested ones,
// leaving what the line says in its own voice.
func outsideBrackets(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
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

// countWords counts runs of three or more letters, so operators, digits
// and one- or two-letter fragments do not pass for words.
func countWords(s string) int {
	n, run := 0, 0
	for _, r := range s + " " {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			run++
			continue
		}
		if run >= 3 {
			n++
		}
		run = 0
	}
	return n
}

// findingSubject names the thing the finding is about: the ecosystem chip
// and "name@version" of the first package the case pins.
func findingSubject(packages []string) (ecosystem, subject string) {
	for _, raw := range packages {
		p, err := domain.ParsePURL(raw)
		if err != nil {
			continue
		}
		if p.Version == "" {
			return p.Ecosystem, p.Name
		}
		return p.Ecosystem, p.Name + "@" + p.Version
	}
	return "", ""
}

// sampleListItem projects a stored sample row onto the website's list row.
func sampleListItem(r serverstore.SampleRow) web.SampleListItem {
	item := web.SampleListItem{
		SampleID:  r.SampleID,
		Status:    r.Status,
		License:   r.License,
		CreatedAt: r.CreatedAt.UTC().Format("2006-01-02"),
	}
	if m, ok := parseManifest(r.ManifestJSON); ok {
		item.Goal = m.Case.Goal
		item.Context = m.Environment.ContextLabel()
		item.Kind = m.Case.Kind
		item.Symbols = m.Symbols
		// A sample names the exact package version it was written against;
		// the list row carries it so the page can file the sample under
		// the version it answers for.
		for _, p := range m.Packages {
			if parsed, err := domain.ParsePURL(p); err == nil && parsed.Version != "" {
				// The whole coordinate, not only the version: a list row and
				// the sitemap both have to be able to name the sample's
				// human-readable canonical URL, and that needs the ecosystem
				// and the name as well.
				item.Ecosystem = parsed.Ecosystem
				item.Name = parsed.Name
				item.Version = parsed.Version
				break
			}
		}
	}
	return item
}

func parseManifest(manifestJSON string) (domain.SampleManifest, bool) {
	var m domain.SampleManifest
	if json.Unmarshal([]byte(manifestJSON), &m) != nil {
		return domain.SampleManifest{}, false
	}
	return m, true
}

func manifestNamesPackage(manifestJSON, prefix string) bool {
	m, ok := parseManifest(manifestJSON)
	if !ok {
		return false
	}
	for _, p := range m.Packages {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// packageHits groups evidence-bearing snapshot targets into package rows,
// ranked by how much the network actually knows about each one.
//
// Ranking matters more than it sounds: a dependency tree is mostly
// transitive packages nobody looks up (accepts, asynckit, bytes …), and
// returning them in discovery order buried axios and express under an
// alphabetical wall of noise.
func (w *webStore) packageHits(ctx context.Context, filter func(p domain.PURL) bool, limit int) ([]web.PackageHit, error) {
	all, err := w.rankedPackages(ctx, filter)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// rankedPackages returns every matching package, most-known-about first:
// symbols with evidence, then raw evidence count, then name for a stable
// order.
func (w *webStore) rankedPackages(ctx context.Context, filter func(p domain.PURL) bool) ([]web.PackageHit, error) {
	targets, err := w.cachedSnapshotTargets(ctx)
	if err != nil {
		return nil, err
	}
	return rankedPackages(targets, filter), nil
}

// rankedPackages is the pure grouping/ranking half of the package inventory.
// Keeping the read outside lets a background refresh use its own query context
// without occupying the foreground target-cache singleflight mutex.
func rankedPackages(targets []serverstore.SnapshotTarget, filter func(p domain.PURL) bool) []web.PackageHit {
	type agg struct {
		hit     web.PackageHit
		symbols map[string]bool
	}
	byPkg := map[string]*agg{}
	for _, t := range targets {
		p, err := domain.ParsePURL(t.PURL)
		if err != nil || (filter != nil && !filter(p)) {
			continue
		}
		key := p.Ecosystem + "/" + p.Name
		a, ok := byPkg[key]
		if !ok {
			a = &agg{hit: web.PackageHit{Ecosystem: p.Ecosystem, Name: p.Name, LatestVersion: p.Version}, symbols: map[string]bool{}}
			byPkg[key] = a
		}
		if domain.CompareVersions(p.Version, a.hit.LatestVersion) > 0 {
			a.hit.LatestVersion = p.Version
		}
		if t.Symbol != "" {
			a.symbols[t.Symbol] = true
		}
		a.hit.EvidenceCount++
	}
	out := make([]web.PackageHit, 0, len(byPkg))
	for _, a := range byPkg {
		a.hit.Symbols = len(a.symbols)
		out = append(out, a.hit)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbols != out[j].Symbols {
			return out[i].Symbols > out[j].Symbols
		}
		if out[i].EvidenceCount != out[j].EvidenceCount {
			return out[i].EvidenceCount > out[j].EvidenceCount
		}
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// RecordPackages returns one page of the record, ranked, with the total
// so the page can say where the reader is.
func (w *webStore) RecordPackages(ctx context.Context, filter web.RecordFilter, offset, limit int) ([]web.PackageHit, int, error) {
	filter.Query = strings.ToLower(strings.TrimSpace(filter.Query))
	all, err := w.rankedRecordPackages(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	total := len(all)
	// A negative offset is a caller bug, but it must not be a panic in a
	// page handler: clamp rather than slice with it.
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, total, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

// recordSnapshot is the subset of a compatibility snapshot needed by the
// record filters. Environment rows and their stage classes are materialized
// already; this adapter reads those facts and does not re-aggregate evidence.
type recordSnapshot struct {
	Rows []struct {
		Env      *domain.EnvironmentFingerprint `json:"envBucket"`
		EnvAlias *domain.EnvironmentFingerprint `json:"env"`
		ByStage  map[string]struct {
			Pass int64 `json:"pass"`
			Fail int64 `json:"fail"`
		} `json:"byStage"`
	} `json:"rows"`
}

func recordStageMatches(byStage map[string]struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
}, basis string) bool {
	if basis == "" {
		return true
	}
	for stage, count := range byStage {
		if count.Pass+count.Fail == 0 {
			continue
		}
		observed := stage == string(domain.StageUsed) || strings.HasPrefix(stage, "PROJECT_")
		if (basis == "observed" && observed) || (basis == "verified" && !observed) {
			return true
		}
	}
	return false
}

func recordSnapshotMatches(raw string, filter web.RecordFilter) bool {
	var doc recordSnapshot
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return false
	}
	for _, row := range doc.Rows {
		env := row.Env
		if env == nil {
			env = row.EnvAlias
		}
		// Selecting an environment dimension requires a recorded
		// fingerprint. An old row with only a presentation label is unknown,
		// not a match inferred from that prose.
		if (filter.OS != "" || filter.Runtime != "") && env == nil {
			continue
		}
		if env != nil && !web.RecordEnvironmentMatches(*env, filter.OS, filter.Runtime) {
			continue
		}
		if recordStageMatches(row.ByStage, filter.Basis) {
			return true
		}
	}
	return false
}

// rankedRecordPackages is the filtered variant of rankedPackages. With only
// a name/ecosystem filter it never loads snapshot documents. OS, runtime and
// basis filters intentionally pay that cost because those dimensions live in
// snapshot rows, and guessing them from npm/PyPI/etc. would be false.
func (w *webStore) rankedRecordPackages(ctx context.Context, filter web.RecordFilter) ([]web.PackageHit, error) {
	type agg struct {
		hit     web.PackageHit
		symbols map[string]bool
		exact   bool
		prefix  bool
	}
	byPkg := map[string]*agg{}
	query := web.ParseRecordQuery(filter.Query)
	add := func(purl, symbol, snapshotJSON string) {
		p, err := domain.ParsePURL(purl)
		if err != nil || (filter.Ecosystem != "" && p.Ecosystem != filter.Ecosystem) {
			return
		}
		queryMatch, exact, prefix := query.MatchPackage(p.Name)
		if !queryMatch {
			return
		}
		if snapshotJSON != "" && !recordSnapshotMatches(snapshotJSON, filter) {
			return
		}
		key := p.Ecosystem + "/" + p.Name
		a, ok := byPkg[key]
		if !ok {
			a = &agg{hit: web.PackageHit{Ecosystem: p.Ecosystem, Name: p.Name, LatestVersion: p.Version}, symbols: map[string]bool{}}
			byPkg[key] = a
		}
		a.exact = a.exact || exact
		a.prefix = a.prefix || prefix
		if domain.CompareVersions(p.Version, a.hit.LatestVersion) > 0 {
			a.hit.LatestVersion = p.Version
		}
		if symbol != "" {
			a.symbols[symbol] = true
		}
		a.hit.EvidenceCount++
	}

	needsSnapshot := filter.OS != "" || filter.Runtime != "" || filter.Basis != ""
	if needsSnapshot {
		rows, err := w.cachedSnapshots(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			add(row.PURL, row.Symbol, row.SnapshotJSON)
		}
	} else {
		targets, err := w.cachedSnapshotTargets(ctx)
		if err != nil {
			return nil, err
		}
		for _, target := range targets {
			add(target.PURL, target.Symbol, "")
		}
	}
	// When a package's evidence last changed. One row per purl, but the
	// query expands every snapshot document to find it — a full pass over
	// compatibility_snapshots, which is why it is read through the cache.
	updated := w.cachedSnapshotUpdatedAt(ctx)
	latest := map[string]time.Time{}
	for purl, at := range updated {
		p, err := domain.ParsePURL(purl)
		if err != nil {
			continue
		}
		key := p.Ecosystem + "/" + p.Name
		if prev, ok := latest[key]; !ok || at.After(prev) {
			latest[key] = at
		}
	}

	out := make([]web.PackageHit, 0, len(byPkg))
	for key, a := range byPkg {
		a.hit.Symbols = len(a.symbols)
		if at, ok := latest[key]; ok {
			a.hit.UpdatedAt = at.UTC().Format("2006-01-02")
		}
		out = append(out, a.hit)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := byPkg[out[i].Ecosystem+"/"+out[i].Name], byPkg[out[j].Ecosystem+"/"+out[j].Name]
		// A typed query still puts what was asked for first; without one
		// this is an inventory of measurements and reads newest first.
		if ai.exact != aj.exact {
			return ai.exact
		}
		if ai.prefix != aj.prefix {
			return ai.prefix
		}
		ti, tj := latest[out[i].Ecosystem+"/"+out[i].Name], latest[out[j].Ecosystem+"/"+out[j].Name]
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		if out[i].EvidenceCount != out[j].EvidenceCount {
			return out[i].EvidenceCount > out[j].EvidenceCount
		}
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (w *webStore) SearchPackages(ctx context.Context, q string, limit int) ([]web.PackageHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	return w.packageHits(ctx, func(p domain.PURL) bool {
		return q == "" || strings.Contains(strings.ToLower(p.Name), q)
	}, limit)
}

const (
	hotPackagesTTL            = time.Minute
	hotPackagesRefreshTimeout = 30 * time.Second
	hotPackagesRetryDelay     = 30 * time.Second
)

func (w *webStore) HotPackages(_ context.Context, limit int) ([]web.PackageHit, error) {
	w.hotMu.Lock()
	now := time.Now()
	rows := w.hotRows
	if !w.hotAt.After(now.Add(-hotPackagesTTL)) &&
		!w.hotRefreshing && !now.Before(w.hotRetryAt) {
		w.hotRefreshing = true
		go w.refreshHotPackages()
	}
	w.hotMu.Unlock()

	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (w *webStore) refreshHotPackages() {
	ctx, cancel := context.WithTimeout(
		serverstore.WithQueryClass(context.Background(), serverstore.ClassBackground),
		hotPackagesRefreshTimeout,
	)
	defer cancel()
	// Keep the complete ranking. The landing asks for twelve rows while the
	// sitemap asks for one hundred; letting the first caller choose the cache
	// size would make the same snapshot mean two different things.
	targets, err := w.s.SnapshotKeys(ctx)
	var rows []web.PackageHit
	if err == nil {
		rows = rankedPackages(targets, nil)
	}

	w.hotMu.Lock()
	defer w.hotMu.Unlock()
	w.hotRefreshing = false
	if err != nil {
		w.hotRetryAt = time.Now().Add(hotPackagesRetryDelay)
		return
	}
	w.hotRows, w.hotAt = rows, time.Now()
	w.hotRetryAt = time.Time{}
}

func (w *webStore) FailureClusters(ctx context.Context, ecosystem, name string) ([]string, int, error) {
	rows, err := w.s.ListFailureClusters(ctx, name)
	if err != nil {
		return nil, 0, err
	}
	// A safety bound, not a display cap. Twelve used to be cut here, before
	// the page had narrowed to a coordinate — so a reader standing on the
	// exact environment where a cluster was recorded saw nothing, because
	// that cluster ranked thirteenth across the whole package. escalade has
	// sixteen: fifteen on windows and the one on linux that the linux
	// coordinate needed. The page does its own bounding, after filtering.
	var out []string
	kept := 0
	matched := 0
	for _, c := range rows {
		if c.Ecosystem != ecosystem {
			continue
		}
		matched++
		if kept >= maxClustersToPage {
			continue
		}
		kept++
		doc := map[string]any{
			// The symbol the cluster is ABOUT. It was never serialized, so
			// the template's {{if .Symbol}} was false on every package page
			// and a failure cluster rendered with no indication of which
			// call it concerned.
			"symbol":              c.Symbol,
			"stage":               c.Stage,
			"errorCode":           c.ErrorCode,
			"fingerprint":         c.ErrorFingerprint,
			"terminationKind":     c.TerminationKind,
			"exitCode":            c.ExitCode,
			"signal":              c.Signal,
			"timeoutMillis":       c.TimeoutMillis,
			"errorSummary":        c.ErrorSummary,
			"evidenceQuality":     c.EvidenceQuality,
			"outerCommands":       c.OuterCommands,
			"actualToolchain":     c.ActualToolchain,
			"stageEvidence":       c.StageEvidence,
			"evidenceGap":         c.FailureEvidenceGap,
			"count":               c.ObservationCount,
			"envSummary":          json.RawMessage(orEmptyObj(c.EnvSummaryJSON)),
			"envVariants":         json.RawMessage(orEmptyArr(c.EnvVariantsJSON)),
			"evidenceBreakdown":   json.RawMessage(orEmptyObj(c.EvidenceBreakdownJSON)),
			"hypotheses":          json.RawMessage(orEmptyArr(c.HypothesesJSON)),
			"regressionCandidate": c.RegressionCandidate,
			"diagnosticCandidate": c.DiagnosticCandidate,
			"versions":            json.RawMessage(orEmptyArr(c.VersionsJSON)),
			"firstSeen":           c.FirstSeen.UTC().Format(time.RFC3339),
			"lastSeen":            c.LastSeen.UTC().Format(time.RFC3339),
		}
		b, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		out = append(out, string(b))
	}
	return out, matched, nil
}

// maxClustersToPage bounds what one package hands the page. It is a guard
// against an unbounded response, not a choice about what to render: the page
// narrows to a coordinate and bounds what is left.
const maxClustersToPage = 500

func orEmptyObj(s string) string {
	if strings.TrimSpace(s) == "" {
		return "{}"
	}
	return s
}

func orEmptyArr(s string) string {
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	return s
}

// TopWanted lists the most-asked packages the network still has no sample
// for. The rows are counted from anonymous miss reports; the question that
// produced each one never left the machine that asked it.
func (w *webStore) TopWanted(ctx context.Context, limit int) ([]web.WantedRow, error) {
	rows, err := w.s.TopWanted(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]web.WantedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.WantedRow{
			Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Symbol: r.Symbol,
			Asks: r.Asks, TargetOS: r.TargetOS, HasPage: r.HasPage,
		})
	}
	return out, nil
}

// PackageAssets carries the release-level census rolled up to packages.
func (w *webStore) PackageAssets(ctx context.Context) ([]web.PackageAsset, error) {
	rows, err := w.s.PackageAssets(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.PackageAsset, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.PackageAsset{
			Ecosystem: r.Ecosystem, Name: r.Name, Releases: r.Releases,
			WithSample: r.WithSample, WithDependency: r.WithDependency,
		})
	}
	return out, nil
}

// CompletenessGaps carries the census's own rows to the page.
//
// The judgement -- which coordinates are backlog, in what order, and which
// axes nothing here can close -- was already made in serverstore, beside the
// matrix it has to agree with. This only changes the type.
func (w *webStore) CompletenessGaps(ctx context.Context, query string, offset, limit int) ([]web.CompletenessGap, int, error) {
	rows, total, err := w.s.CompletenessGaps(ctx, query, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]web.CompletenessGap, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.CompletenessGap{
			Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version,
			HasSample: r.HasSample, HasEvidence: r.HasEvidence,
			Dependency:     r.Dependency,
			SampleNAReason: r.SampleNAReason, DependencyNAReason: r.DependencyNAReason,
		})
	}
	return out, total, nil
}

func (w *webStore) WantedForPackage(ctx context.Context, ecosystem, name string) ([]web.WantedRow, error) {
	rows, err := w.s.WantedForPackage(ctx, ecosystem, name)
	if err != nil {
		return nil, err
	}
	out := make([]web.WantedRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.WantedRow{
			Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Symbol: r.Symbol,
			Asks: r.Asks, HasPage: true,
		})
	}
	return out, nil
}

func (w *webStore) DependencySubjects(ctx context.Context, query string, offset, limit int) ([]web.DependencySubject, int, error) {
	rows, total, err := w.s.DependencySubjects(ctx, query, offset, limit)
	if err != nil {
		return nil, 0, err
	}
	out := make([]web.DependencySubject, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.DependencySubject{
			Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version,
			Parents: int64(r.Parents), Projects: int64(r.Projects),
		})
	}
	return out, total, nil
}

func (w *webStore) DependencyParents(ctx context.Context, ecosystem, name, version string) ([]web.DependencyEdge, error) {
	rows, err := w.s.DependencyParents(ctx, ecosystem, name, version)
	if err != nil {
		return nil, err
	}
	out := make([]web.DependencyEdge, 0, len(rows))
	for _, r := range rows {
		out = append(out, web.DependencyEdge{
			ParentName: r.ParentName, ParentVersion: r.ParentVersion,
			ChildName: r.ChildName, ChildVersion: r.ChildVersion,
			Projects: int64(r.Projects),
		})
	}
	return out, nil
}

func (w *webStore) DependencyResolvedNone(ctx context.Context, ecosystem, name, version string) (bool, error) {
	return w.s.DependencyResolvedNone(ctx, ecosystem, name, version)
}

// SampleSource reads the artifact and returns its readable files.
//
// The blob is the same one /v1/samples/{id}/artifact serves, and the same
// quarantine rule applies by construction: this is only reached from a sample
// page, and a quarantined sample has no page.
func (w *webStore) SampleSource(ctx context.Context, id string) ([]web.SampleFile, error) {
	if w.blobs == nil {
		return nil, nil
	}
	rc, err := w.blobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	tgz, err := io.ReadAll(io.LimitReader(rc, samples.MaxCompressedBytes+1))
	if err != nil {
		return nil, err
	}
	files, err := samples.ReadTextFiles(tgz)
	if err != nil {
		return nil, err
	}
	out := make([]web.SampleFile, 0, len(files))
	for _, f := range files {
		out = append(out, web.SampleFile{Name: f.Name, Body: f.Body, Truncated: f.Truncated})
	}
	return out, nil
}
