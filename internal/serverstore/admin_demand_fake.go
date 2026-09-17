package serverstore

import (
	"context"
	"encoding/json"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

var _ AdminDemandReader = (*Fake)(nil)

// AdminDemand mirrors the PostgreSQL read over the fake's ledgers: the
// per-reporter/day wanted rows, the manifests' package lists and the
// search hit and miss ledgers.
func (f *Fake) AdminDemand(_ context.Context, now time.Time) (AdminDemand, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	today, start, priorStart, _ := adminDemandWindow(now)
	startKey, todayKey, priorKey := start.Format("2006-01-02"), today.Format("2006-01-02"), priorStart.Format("2006-01-02")
	out := AdminDemand{WindowStart: startKey, WindowEnd: todayKey}

	type packageKey struct{ ecosystem, name string }
	packages := map[packageKey]*AdminDemandPackage{}
	coordinateSets := map[packageKey]map[[3]string]bool{}
	coordinates := map[[5]string]*AdminDemandCoordinate{}
	for seen := range f.wantedSeen {
		epoch := seen[5]
		key := packageKey{seen[0], seen[1]}
		if epoch >= priorKey && epoch < startKey {
			row := packages[key]
			if row == nil {
				row = &AdminDemandPackage{Ecosystem: seen[0], Name: seen[1]}
				packages[key] = row
			}
			row.PriorImpact++
			continue
		}
		if epoch < startKey || epoch > todayKey {
			continue
		}
		out.TotalImpact++
		row := packages[key]
		if row == nil {
			row = &AdminDemandPackage{Ecosystem: seen[0], Name: seen[1]}
			packages[key] = row
		}
		row.Impact++
		if epoch > row.LastDay {
			row.LastDay = epoch
		}
		if coordinateSets[key] == nil {
			coordinateSets[key] = map[[3]string]bool{}
		}
		coordinateSets[key][[3]string{seen[2], seen[3], seen[4]}] = true
		coordinate := [5]string{seen[0], seen[1], seen[2], seen[3], seen[4]}
		c := coordinates[coordinate]
		if c == nil {
			c = &AdminDemandCoordinate{Ecosystem: seen[0], Name: seen[1], Version: seen[2], Symbol: seen[3], TargetOS: seen[4]}
			coordinates[coordinate] = c
		}
		c.Impact++
		if epoch > c.LastDay {
			c.LastDay = epoch
		}
	}
	var candidates []AdminDemandPackage
	for key, row := range packages {
		if row.Impact == 0 {
			continue
		}
		out.PackageCount++
		row.Coordinates = int64(len(coordinateSets[key]))
		row.Samples = f.liveSamplesForPackageLocked(key.ecosystem, key.name)
		candidates = append(candidates, *row)
	}
	sortAdminDemandPackages(candidates)
	if len(candidates) > adminDemandGapCandidates {
		candidates = candidates[:adminDemandGapCandidates]
	}
	out.Packages, out.Gaps = adminDemandSplit(candidates)

	misses := make([]AdminDemandCoordinate, 0, len(coordinates))
	for _, c := range coordinates {
		misses = append(misses, *c)
	}
	sortAdminDemandCoordinates(misses)
	if len(misses) > adminDemandMissLimit {
		misses = misses[:adminDemandMissLimit]
	}
	out.TopMisses = misses

	counts := map[string]AdminDemandSearchDay{}
	for _, hit := range f.searchHits {
		day := counts[hit.Epoch]
		day.Hits++
		counts[hit.Epoch] = day
	}
	for _, epoch := range f.searchMisses {
		day := counts[epoch]
		day.Misses++
		counts[epoch] = day
	}
	out.Search, out.Week, out.PriorWeek = adminDemandSearchSeries(counts, today, start)
	return out, nil
}

func (f *Fake) liveSamplesForPackageLocked(ecosystem, name string) int64 {
	var n int64
	for _, s := range f.samples {
		if s.Quarantined {
			continue
		}
		var m struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(s.ManifestJSON), &m) != nil {
			continue
		}
		for _, raw := range m.Packages {
			purl, err := domain.ParsePURL(raw)
			if err == nil && purl.Ecosystem == ecosystem && purl.Name == name {
				n++
				break
			}
		}
	}
	return n
}
