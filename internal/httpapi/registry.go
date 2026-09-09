package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// handleRegistryPackage implements GET /v1/registry/packages/{purl}.
// The purl arrives path-escaped ("pkg:npm%2Faxios@1.12.0"). Reads touch the
// packages table and materialized snapshots ONLY — no in-request
// aggregation (goal.md §14.5).
func (a *api) handleRegistryPackage(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("purl")
	if strings.Contains(raw, "%") {
		if un, err := url.PathUnescape(raw); err == nil {
			raw = un
		}
	}
	p, err := domain.ParsePURL(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid purl: "+err.Error())
		return
	}
	canonical := p.String()

	pkg, ok, err := a.d.Store.GetPackage(r.Context(), canonical)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "package lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "package not known")
		return
	}

	versions, err := a.d.Store.ListPackageVersions(r.Context(), p.Ecosystem, p.Name)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "version listing failed")
		return
	}
	majorSet := map[string]bool{}
	for _, v := range versions {
		if v.Major != "" {
			majorSet[v.Major] = true
		}
	}
	majors := make([]string, 0, len(majorSet))
	for m := range majorSet {
		majors = append(majors, m)
	}
	sort.Strings(majors)

	symbols, err := a.symbolsForPURL(r, canonical)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "symbol listing failed")
		return
	}

	var snapshotSummary json.RawMessage
	if js, ok, err := a.d.Store.GetSnapshot(r.Context(), canonical, ""); err == nil && ok {
		snapshotSummary = json.RawMessage(js)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"purl":            canonical,
		"publicness":      pkg.Publicness,
		"majors":          majors,
		"symbols":         symbols,
		"snapshotSummary": snapshotSummary,
	})
}

// symbolsForPURL lists the distinct symbol families with evidence for one
// package version, from the snapshot target index.
func (a *api) symbolsForPURL(r *http.Request, purl string) ([]string, error) {
	targets, err := a.d.Store.ListSnapshotTargets(r.Context())
	if err != nil {
		return nil, err
	}
	symbols := []string{}
	for _, t := range targets {
		if t.PURL == purl && t.Symbol != "" {
			symbols = append(symbols, t.Symbol)
		}
	}
	sort.Strings(symbols)
	return symbols, nil
}

// registrySnapshotBatch bounds how many versions share one snapshot lookup.
// A package can have hundreds of releases; one page per bounded slice keeps
// the read from becoming one array parameter the size of its history.
const registrySnapshotBatch = 200

// handleRegistrySymbol implements
// GET /v1/registry/symbols/{ecosystem}/{package...}/{family}.
// golang package names contain slashes, so the tail is split as
// package-path + final family segment.
func (a *api) handleRegistrySymbol(w http.ResponseWriter, r *http.Request) {
	ecosystem := r.PathValue("ecosystem")
	rest := r.PathValue("rest")
	segs := strings.Split(rest, "/")
	if len(segs) < 2 {
		writeErr(w, http.StatusBadRequest, "path must be /v1/registry/symbols/{ecosystem}/{package}/{family}")
		return
	}
	family := segs[len(segs)-1]
	pkgName := strings.Join(segs[:len(segs)-1], "/")
	if un, err := url.PathUnescape(pkgName); err == nil {
		pkgName = un
	}
	if un, err := url.PathUnescape(family); err == nil {
		family = un
	}

	versions, err := a.d.Store.ListPackageVersions(r.Context(), ecosystem, pkgName)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "version listing failed")
		return
	}

	found, err := a.familySnapshots(r.Context(), versions, family)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "snapshot lookup failed")
		return
	}
	type versionSnapshot struct {
		PURL     string          `json:"purl"`
		Snapshot json.RawMessage `json:"snapshot"`
	}
	snapshots := []versionSnapshot{}
	for _, v := range versions {
		if js, ok := found[v.PURL]; ok {
			snapshots = append(snapshots, versionSnapshot{PURL: v.PURL, Snapshot: json.RawMessage(js)})
		}
	}
	if len(snapshots) == 0 {
		writeErr(w, http.StatusNotFound, "no evidence for this symbol")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ecosystem": ecosystem,
		"package":   pkgName,
		"family":    family,
		"snapshots": snapshots,
	})
}

// snapshotPagesStore is the bounded-page form of GetSnapshot for one symbol
// across many releases. PostgreSQL offers it; the fallback keeps the
// row-at-a-time contract for stores that do not.
type snapshotPagesStore interface {
	SnapshotsForPURLs(ctx context.Context, purls []string, symbol string) (map[string]string, error)
}

// familySnapshots reads the family's snapshot for every listed release,
// keyed by purl. Releases without one are absent from the map; the caller
// walks the version listing, so the document keeps the listing's order.
//
// It reads a bounded page of releases per checkout rather than one release
// per checkout. A library with three hundred releases was three hundred
// interactive checkouts for one public read (#174).
//
// A read the store refuses is returned, not skipped. Skipping it turned a
// busy pool into "no evidence for this symbol" -- a 404 that is false and
// that a client would cache.
func (a *api) familySnapshots(ctx context.Context, versions []serverstore.PackageRow, family string) (map[string]string, error) {
	found := make(map[string]string, len(versions))
	bulk, ok := a.d.Store.(snapshotPagesStore)
	if !ok {
		for _, v := range versions {
			js, ok, err := a.d.Store.GetSnapshot(ctx, v.PURL, family)
			if err != nil {
				return nil, err
			}
			if ok {
				found[v.PURL] = js
			}
		}
		return found, nil
	}
	purls := make([]string, 0, len(versions))
	for _, v := range versions {
		purls = append(purls, v.PURL)
	}
	for start := 0; start < len(purls); start += registrySnapshotBatch {
		end := min(start+registrySnapshotBatch, len(purls))
		page, err := bulk.SnapshotsForPURLs(ctx, purls[start:end], family)
		if err != nil {
			return nil, err
		}
		for purl, js := range page {
			found[purl] = js
		}
	}
	return found, nil
}
