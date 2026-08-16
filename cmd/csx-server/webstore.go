package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
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
	// Newest activity first, then newest version. The SQL tiebreak is
	// ORDER BY version DESC, which is a string sort: it puts 7.0.3 above
	// 14.0.1. This ordering is the one the reader sees, so it decides.
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].LastSeen.Equal(rows[j].LastSeen) {
			return rows[i].LastSeen.After(rows[j].LastSeen)
		}
		return domain.CompareVersions(rows[i].Version, rows[j].Version) > 0
	})
	// Only versions that HAVE a page. The list came from the packages
	// table, which the publicness gate also writes to -- including purls
	// whose evidence batch was then refused -- while the version page 404s
	// unless that exact version has a snapshot target. So a package page
	// listed versions under a heading whose empty state reads "No versions
	// with evidence yet", and every one of those links was a 404.
	//
	// One extra scan per package page, the same one PackageSymbols already
	// pays per version page. A link into a 404 is worse than a slow page.
	targets, terr := w.s.ListSnapshotTargets(ctx)
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

func (w *webStore) PackageSymbols(ctx context.Context, ecosystem, name, version string) ([]string, error) {
	targets, err := w.s.ListSnapshotTargets(ctx)
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
	prefix := strings.TrimSuffix(
		domain.PURL{Ecosystem: ecosystem, Name: name, Version: ""}.String(), "")
	rows, err := w.s.SamplesForPackages(ctx, []string{prefix + "%"}, limit)
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
	targets, err := w.s.ListSnapshotTargets(ctx)
	if err != nil {
		return nil, err
	}
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
	return out, nil
}

// RecordPackages returns one page of the record, ranked, with the total
// so the page can say where the reader is.
func (w *webStore) RecordPackages(ctx context.Context, q string, offset, limit int) ([]web.PackageHit, int, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	all, err := w.rankedPackages(ctx, func(p domain.PURL) bool {
		return q == "" || strings.Contains(strings.ToLower(p.Name), q)
	})
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

func (w *webStore) SearchPackages(ctx context.Context, q string, limit int) ([]web.PackageHit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	return w.packageHits(ctx, func(p domain.PURL) bool {
		return q == "" || strings.Contains(strings.ToLower(p.Name), q)
	}, limit)
}

func (w *webStore) HotPackages(ctx context.Context, limit int) ([]web.PackageHit, error) {
	return w.packageHits(ctx, nil, limit)
}

func (w *webStore) FailureClusters(ctx context.Context, ecosystem, name string) ([]string, error) {
	rows, err := w.s.ListFailureClusters(ctx, name)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, c := range rows {
		if c.Ecosystem != ecosystem {
			continue
		}
		doc := map[string]any{
			// The symbol the cluster is ABOUT. It was never serialized, so
			// the template's {{if .Symbol}} was false on every package page
			// and a failure cluster rendered with no indication of which
			// call it concerned.
			"symbol":              c.Symbol,
			"stage":               c.Stage,
			"errorCode":           c.ErrorCode,
			"fingerprint":         c.ErrorFingerprint,
			"count":               c.ObservationCount,
			"envSummary":          json.RawMessage(orEmptyObj(c.EnvSummaryJSON)),
			"hypotheses":          json.RawMessage(orEmptyArr(c.HypothesesJSON)),
			"regressionCandidate": c.RegressionCandidate,
			"versions":            json.RawMessage(orEmptyArr(c.VersionsJSON)),
		}
		b, err := json.Marshal(doc)
		if err != nil {
			continue
		}
		out = append(out, string(b))
	}
	return out, nil
}

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
			Ecosystem: r.Ecosystem, Name: r.Name, Symbol: r.Symbol,
			Asks: r.Asks, HasPage: r.HasPage,
		})
	}
	return out, nil
}
