package web

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// assetTTL, assetRefreshTimeout and assetRetryDelay pace the package rollup.
//
// The rollup classifies every public release, which is fine on a timer and not
// fine inside a request: the first /compatibility after a restart would wait
// on a whole-corpus scan while a fresh builder holds the pool, which is how
// the findings page once timed out a deployment smoke without sending a byte.
const (
	assetTTL            = 5 * time.Minute
	assetRefreshTimeout = 30 * time.Second
	assetRetryDelay     = 30 * time.Second
)

// assetCache is the per-package rollup, keyed "ecosystem/name".
type assetCache struct {
	mu         sync.Mutex
	rows       map[string]PackageAsset
	at         time.Time
	refreshing bool
	retryAt    time.Time
}

// packageAssets returns the last complete rollup and starts one bounded
// refresh when it is cold or stale.
//
// A cold miss returns nothing rather than blocking, and the page renders every
// axis as unknown-yet. That is the honest reading of "we have not looked":
// waiting would trade a missing chip for a page that does not arrive.
func (s *site) packageAssets() map[string]PackageAsset {
	s.assets.mu.Lock()
	now := time.Now()
	rows := s.assets.rows
	if !s.assets.at.After(now.Add(-assetTTL)) &&
		!s.assets.refreshing && !now.Before(s.assets.retryAt) {
		s.assets.refreshing = true
		go s.refreshPackageAssets(assetRefreshTimeout)
	}
	s.assets.mu.Unlock()
	return rows
}

func (s *site) refreshPackageAssets(timeout time.Duration) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("web: panic refreshing package assets: %v", recovered)
			s.failPackageAssetRefresh()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	rows, err := s.d.Store.PackageAssets(ctx)
	if err != nil {
		s.failPackageAssetRefresh()
		return
	}
	out := make(map[string]PackageAsset, len(rows))
	for _, a := range rows {
		out[a.Ecosystem+"/"+a.Name] = a
	}
	s.assets.mu.Lock()
	s.assets.rows, s.assets.at = out, time.Now()
	s.assets.refreshing = false
	s.assets.retryAt = time.Time{}
	s.assets.mu.Unlock()
}

// failPackageAssetRefresh keeps the last good rollup and backs off.
//
// The stale map is better than an empty one: it was true recently, and
// dropping it would turn one failed background read into a page that reports
// every package as unmeasured.
func (s *site) failPackageAssetRefresh() {
	s.assets.mu.Lock()
	s.assets.refreshing = false
	s.assets.retryAt = time.Now().Add(assetRetryDelay)
	s.assets.mu.Unlock()
}

// findingPackages is the set of packages a derived finding names, keyed
// "ecosystem/name".
//
// Read from the findings cache rather than recomputed: what counts as a
// finding is decided by walking a manifest's case, and a second definition
// written in SQL to look like it is how two pages come to disagree about the
// same sample.
func findingPackages(rows []finding) map[string]bool {
	out := make(map[string]bool, len(rows))
	for _, f := range rows {
		name := f.Subject
		if i := strings.LastIndex(name, "@"); i > 0 {
			name = name[:i]
		}
		if name == "" || f.Ecosystem == "" {
			continue
		}
		out[f.Ecosystem+"/"+name] = true
	}
	return out
}

// compatAxis is one of the four things a reader wants to know about a package.
type compatAxis struct {
	Label string
	Text  string
	// Held is true when this network has the asset for every release it
	// knows of. It drives emphasis only; Text carries the actual answer.
	Held bool
	// Partial is true when some releases have it and others do not, which is
	// the state most of the corpus is in and the one a flag would erase.
	Partial bool
}

// compatRow is one package as the collection renders it.
type compatRow struct {
	PackageHit
	Href string
	Axes [4]compatAxis
}

// compatRowFor decorates one package hit with what the network holds.
//
// The counts are ratios over releases, deliberately. "This package has a
// sample" is true of one sample across fifty releases and a reader takes it to
// mean covered; the ratio says the same thing without the overstatement. The
// entry count the list used to lead with stays on the row and stops being the
// headline: it counts rows in this network's own bookkeeping, so a library
// with four hundred of them and no contract outranked one with three and a
// passing one.
func compatRowFor(lang string, base basePage, hit PackageHit, asset PackageAsset, known, hasFinding bool) compatRow {
	row := compatRow{PackageHit: hit, Href: base.WithLang(pkgHref(hit.Ecosystem, hit.Name))}
	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }

	ratio := func(label string, have, of int) compatAxis {
		a := compatAxis{Label: i18n.T(lang, label)}
		switch {
		case !known || of == 0:
			a.Text = i18n.T(lang, "compatibility.axis_unknown")
		case have == 0:
			a.Text = i18n.T(lang, "compatibility.axis_none")
		case have >= of:
			a.Text = i18n.T(lang, "compatibility.axis_all", n(of))
			a.Held = true
		default:
			a.Text = i18n.T(lang, "compatibility.axis_some", n(have), n(of))
			a.Partial = true
		}
		return a
	}
	row.Axes[0] = ratio("compatibility.axis_sample", asset.WithSample, asset.Releases)
	// Evidence is what puts a package in this collection at all: every row
	// here came from a compatibility snapshot, which is an observation. It is
	// stated rather than counted so the row does not imply a second, smaller
	// corpus.
	row.Axes[1] = compatAxis{
		Label: i18n.T(lang, "compatibility.axis_evidence"),
		Text:  i18n.T(lang, "compatibility.evidence_observed"), Held: true,
	}
	// The dependency axis is not askable in every ecosystem: nothing here
	// ships a scanner for golang, maven, gem, pub, hex or composer. "None
	// yet" on those rows promises a gap somebody could close, and the census
	// already subtracts them from the backlog -- so the collection has to
	// make the same distinction or it contradicts /gaps about one package.
	if _, unaskable := domain.DependencyNotApplicable(hit.Ecosystem); unaskable {
		row.Axes[2] = compatAxis{
			Label: i18n.T(lang, "compatibility.axis_dependency"),
			Text:  i18n.T(lang, "compatibility.dep_unaskable"),
		}
	} else {
		row.Axes[2] = ratio("compatibility.axis_dependency", asset.WithDependency, asset.Releases)
	}
	finding := compatAxis{Label: i18n.T(lang, "compatibility.axis_finding")}
	if hasFinding {
		finding.Text, finding.Held = i18n.T(lang, "compatibility.finding_yes"), true
	} else {
		finding.Text = i18n.T(lang, "compatibility.finding_none")
	}
	row.Axes[3] = finding
	return row
}

// recordsGone sends the retired inventory address to the collection that
// replaced it.
func recordsGone(w http.ResponseWriter, r *http.Request) {
	target := "/compatibility"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
