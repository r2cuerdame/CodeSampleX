package serverstore

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// AdminDemandDays is the demand window the package and search panels
	// show; AdminDemandSeriesDays is how far back the daily search series
	// goes so a week can be compared with the week before it.
	AdminDemandDays          = 7
	AdminDemandSeriesDays    = 14
	adminDemandPackageLimit  = 15
	adminDemandGapLimit      = 10
	adminDemandGapCandidates = 60
	adminDemandMissLimit     = 10
	// AdminDemandGapSamples is the sample count at or below which a package
	// that people keep asking for counts as a gap. Two samples cover at most
	// two of a package's symbols; a package in the demand top with that
	// little is where the next sample should go.
	AdminDemandGapSamples = 2
)

// AdminDemandReader is discovered dynamically by the dashboard, like
// AdminInsightsReader, so package demand reporting does not widen the
// public Store contract.
type AdminDemandReader interface {
	AdminDemand(ctx context.Context, now time.Time) (AdminDemand, error)
}

// AdminDemandPackage is one package's recent unanswered demand beside the
// number of live samples that carry it. Impact counts retained
// reporter/day coordinate rows in the window (the same unit as the coverage
// map), never raw searches or people; PriorImpact is the window before.
type AdminDemandPackage struct {
	Ecosystem   string
	Name        string
	Impact      int64
	PriorImpact int64
	Coordinates int64
	Samples     int64
	LastDay     string
}

// AdminDemandCoordinate is one requested coordinate that returned no
// answer in the window, ranked by the same impact unit.
type AdminDemandCoordinate struct {
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	TargetOS  string
	Impact    int64
	LastDay   string
}

// AdminDemandSearchDay is one UTC day of deduplicated search outcomes:
// hits reported by clients and misses (Wanted reports), the two halves of
// the no-result rate in the same unit.
type AdminDemandSearchDay struct {
	Day    string
	Hits   int64
	Misses int64
}

// AdminDemandSearchWindow is one rolling seven-day window of the same.
type AdminDemandSearchWindow struct {
	Hits   int64
	Misses int64
}

// Total is the denominator of the no-result rate.
func (w AdminDemandSearchWindow) Total() int64 { return w.Hits + w.Misses }

// AdminDemand is the bounded read behind the package and search halves of
// the demand diagnostics panel.
type AdminDemand struct {
	WindowStart string
	WindowEnd   string
	// Packages is the demand top; Gaps is the subset of the wider candidate
	// set with at most AdminDemandGapSamples live samples.
	Packages []AdminDemandPackage
	Gaps     []AdminDemandPackage
	// TotalImpact and PackageCount describe the whole window, not only the
	// rows shown.
	TotalImpact  int64
	PackageCount int64
	// TopMisses is the highest-impact unanswered coordinates.
	TopMisses []AdminDemandCoordinate
	// Search is the daily series over AdminDemandSeriesDays, oldest first,
	// zero-filled; Week and PriorWeek are the two rolling windows.
	Search    []AdminDemandSearchDay
	Week      AdminDemandSearchWindow
	PriorWeek AdminDemandSearchWindow
}

var _ AdminDemandReader = (*PG)(nil)

func adminDemandWindow(now time.Time) (today, start, priorStart, seriesStart time.Time) {
	now = now.UTC()
	today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start = today.AddDate(0, 0, -(AdminDemandDays - 1))
	priorStart = start.AddDate(0, 0, -AdminDemandDays)
	seriesStart = today.AddDate(0, 0, -(AdminDemandSeriesDays - 1))
	return today, start, priorStart, seriesStart
}

// AdminDemand reads the seven-day demand ledger through the epoch index
// 0035 added, joins only the bounded candidate set to the sample
// projection, and counts search outcomes by their UTC epoch.
func (p *PG) AdminDemand(ctx context.Context, now time.Time) (AdminDemand, error) {
	today, start, priorStart, seriesStart := adminDemandWindow(now)
	out := AdminDemand{
		WindowStart: start.Format("2006-01-02"),
		WindowEnd:   today.Format("2006-01-02"),
	}
	err := p.withConn(ctx, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `
			WITH recent AS MATERIALIZED (
				SELECT ecosystem, name, version, symbol, target_os, epoch
				  FROM wanted_dedup
				 WHERE epoch >= $1 AND epoch <= $2
			), prior AS MATERIALIZED (
				SELECT ecosystem, name, COUNT(*)::bigint AS impact
				  FROM wanted_dedup
				 WHERE epoch >= $3 AND epoch < $1
				 GROUP BY ecosystem, name
			), ranked AS (
				SELECT r.ecosystem, r.name,
				       COUNT(*)::bigint AS impact,
				       COUNT(DISTINCT (r.version, r.symbol, r.target_os))::bigint AS coordinates,
				       MAX(r.epoch) AS last_day
				  FROM recent r
				 GROUP BY r.ecosystem, r.name
				 ORDER BY impact DESC, r.ecosystem, r.name
				 LIMIT $4
			)
			SELECT k.ecosystem, k.name, k.impact, COALESCE(pr.impact, 0), k.coordinates, k.last_day,
			       (SELECT COUNT(DISTINCT sp.sample_id)
			          FROM packages pkg
			          JOIN sample_packages sp ON sp.purl = pkg.purl
			          JOIN samples s ON s.sample_id = sp.sample_id
			         WHERE pkg.ecosystem = k.ecosystem AND pkg.name = k.name
			           AND NOT s.quarantined)::bigint AS samples
			  FROM ranked k
			  LEFT JOIN prior pr ON pr.ecosystem = k.ecosystem AND pr.name = k.name
			 ORDER BY k.impact DESC, k.ecosystem, k.name`,
			start.Format("2006-01-02"), today.Format("2006-01-02"), priorStart.Format("2006-01-02"), adminDemandGapCandidates)
		if err != nil {
			return err
		}
		var candidates []AdminDemandPackage
		for rows.Next() {
			var row AdminDemandPackage
			if err := rows.Scan(&row.Ecosystem, &row.Name, &row.Impact, &row.PriorImpact, &row.Coordinates, &row.LastDay, &row.Samples); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		out.Packages, out.Gaps = adminDemandSplit(candidates)

		if err := conn.QueryRow(ctx, `
			SELECT COUNT(*)::bigint, COUNT(DISTINCT (ecosystem, name))::bigint
			  FROM wanted_dedup WHERE epoch >= $1 AND epoch <= $2`,
			start.Format("2006-01-02"), today.Format("2006-01-02")).Scan(&out.TotalImpact, &out.PackageCount); err != nil {
			return err
		}

		rows, err = conn.Query(ctx, `
			SELECT ecosystem, name, version, symbol, target_os, COUNT(*)::bigint AS impact, MAX(epoch)
			  FROM wanted_dedup
			 WHERE epoch >= $1 AND epoch <= $2
			 GROUP BY ecosystem, name, version, symbol, target_os
			 ORDER BY impact DESC, ecosystem, name, version, symbol, target_os
			 LIMIT $3`,
			start.Format("2006-01-02"), today.Format("2006-01-02"), adminDemandMissLimit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row AdminDemandCoordinate
			if err := rows.Scan(&row.Ecosystem, &row.Name, &row.Version, &row.Symbol, &row.TargetOS, &row.Impact, &row.LastDay); err != nil {
				rows.Close()
				return err
			}
			out.TopMisses = append(out.TopMisses, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = conn.Query(ctx, `
			SELECT epoch, 'hit'::text, COUNT(*)::bigint FROM search_hits WHERE epoch >= $1 AND epoch <= $2 GROUP BY epoch
			UNION ALL
			SELECT epoch, 'miss'::text, COUNT(*)::bigint FROM search_misses WHERE epoch >= $1 AND epoch <= $2 GROUP BY epoch`,
			seriesStart.Format("2006-01-02"), today.Format("2006-01-02"))
		if err != nil {
			return err
		}
		defer rows.Close()
		counts := map[string]AdminDemandSearchDay{}
		for rows.Next() {
			var epoch, kind string
			var n int64
			if err := rows.Scan(&epoch, &kind, &n); err != nil {
				return err
			}
			day := counts[epoch]
			if kind == "hit" {
				day.Hits += n
			} else {
				day.Misses += n
			}
			counts[epoch] = day
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Search, out.Week, out.PriorWeek = adminDemandSearchSeries(counts, today, start)
		return nil
	})
	if err != nil {
		return AdminDemand{}, err
	}
	return out, nil
}

// adminDemandSplit keeps the demand top and separately the gap rows: the
// candidates with at most AdminDemandGapSamples live samples, in demand
// order.
func adminDemandSplit(candidates []AdminDemandPackage) (packages, gaps []AdminDemandPackage) {
	for i, row := range candidates {
		if i < adminDemandPackageLimit {
			packages = append(packages, row)
		}
		if row.Samples <= AdminDemandGapSamples && len(gaps) < adminDemandGapLimit {
			gaps = append(gaps, row)
		}
	}
	return packages, gaps
}

// adminDemandSearchSeries zero-fills the series and folds the two rolling
// windows. A day with no row is a measured zero here: hits and misses are
// both event tables, and no event on a day is what an idle day looks like.
func adminDemandSearchSeries(counts map[string]AdminDemandSearchDay, today, weekStart time.Time) ([]AdminDemandSearchDay, AdminDemandSearchWindow, AdminDemandSearchWindow) {
	series := make([]AdminDemandSearchDay, 0, AdminDemandSeriesDays)
	var week, prior AdminDemandSearchWindow
	first := today.AddDate(0, 0, -(AdminDemandSeriesDays - 1))
	weekKey := weekStart.Format("2006-01-02")
	for i := 0; i < AdminDemandSeriesDays; i++ {
		key := first.AddDate(0, 0, i).Format("2006-01-02")
		day := counts[key]
		day.Day = key
		series = append(series, day)
		if key >= weekKey {
			week.Hits += day.Hits
			week.Misses += day.Misses
		} else {
			prior.Hits += day.Hits
			prior.Misses += day.Misses
		}
	}
	return series, week, prior
}

func sortAdminDemandCoordinates(rows []AdminDemandCoordinate) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Impact != b.Impact {
			return a.Impact > b.Impact
		}
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.TargetOS < b.TargetOS
	})
}

func sortAdminDemandPackages(rows []AdminDemandPackage) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Impact != rows[j].Impact {
			return rows[i].Impact > rows[j].Impact
		}
		if rows[i].Ecosystem != rows[j].Ecosystem {
			return rows[i].Ecosystem < rows[j].Ecosystem
		}
		return rows[i].Name < rows[j].Name
	})
}
