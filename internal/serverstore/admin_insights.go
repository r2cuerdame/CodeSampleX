package serverstore

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// AdminInsightDays is the number of UTC calendar days shown by the
	// private operator dashboard. The stats read includes one additional
	// baseline day so the first visible day's delta can be calculated.
	AdminInsightDays     = 30
	adminTopPackageLimit = 10
)

// AdminInsightsReader is intentionally separate from Store. The private
// dashboard discovers this optional read capability dynamically, so adding
// operational reporting does not widen the public server persistence
// contract or force every test fake to implement it.
type AdminInsightsReader interface {
	AdminInsights(ctx context.Context, now time.Time) (AdminInsights, error)
}

// AdminMetricValue preserves the difference between a measured zero and a
// missing/malformed field in an old stats_daily document.
type AdminMetricValue struct {
	Value int64
	Valid bool
}

// AdminDailyStat is one stored UTC snapshot. Missing days are not synthesized.
type AdminDailyStat struct {
	Day             time.Time
	Evidence        AdminMetricValue
	VerifiedSamples AdminMetricValue
	Packages        AdminMetricValue
}

// AdminVerificationCounts is all verification receipt activity in the
// displayed network window. It deliberately does not claim to isolate the
// private sample factory from other contributors.
type AdminVerificationCounts struct {
	Pass         int64
	Fail         int64
	Skipped      int64
	Unclassified int64
}

// Total returns every receipt counted in the window.
func (c AdminVerificationCounts) Total() int64 {
	return c.Pass + c.Fail + c.Skipped + c.Unclassified
}

// AdminEcosystemCount counts 30-day PASS receipts by the environment recorded
// on the receipt itself. It never substitutes the sample author's manifest
// environment for where a matrix verification actually ran. The PG reader
// returns only nine fixed supported labels plus "other", never raw input.
type AdminEcosystemCount struct {
	Ecosystem     string
	Verifications int64
}

// AdminPackageDepth is one of the most deeply covered package names among
// v2 PASS receipts with resolvedPackages in the last 30 UTC calendar days.
// Versions are collapsed and one sample counts once per package. V1 receipts
// establish no resolved version and are intentionally absent.
type AdminPackageDepth struct {
	Ecosystem       string
	Name            string
	VerifiedSamples int64
}

// AdminSearchOutcomeCounts is the bounded aggregate of successful public
// search responses recorded in the dashboard window. Available distinguishes
// no collected rows from a measured zero; in normal operation every row has
// at least one outcome. Dates are UTC calendar dates.
type AdminSearchOutcomeCounts struct {
	Available  bool
	SampleHits int64
	NoMatches  int64
	Days       int64
	FirstDay   string
	LastDay    string
}

func (c AdminSearchOutcomeCounts) Total() int64 { return c.SampleHits + c.NoMatches }

// AdminJobReasonCounts separates work a verifier may claim now from live
// leases and abandoned leases. Stale jobs are also claimable; they remain a
// separate count because a growing stale share is an operational fault signal.
type AdminJobReasonCounts struct {
	Claimable int64
	Live      int64
	Stale     int64
}

// AdminJobQueue is an aggregate of verification_jobs. It deliberately never
// returns claimed_by: the dashboard needs workload and lease health, not a
// list of contributor identities. LiveClaimants is the number of distinct
// anonymous IDs holding a non-expired lease, not the number of online workers.
type AdminJobQueue struct {
	Cross           AdminJobReasonCounts
	Matrix          AdminJobReasonCounts
	Other           AdminJobReasonCounts
	LiveClaimants   int64
	OldestClaimable time.Time
	HasOldest       bool
}

func (q AdminJobQueue) Claimable() int64 {
	return q.Cross.Claimable + q.Matrix.Claimable + q.Other.Claimable
}

func (q AdminJobQueue) Live() int64 {
	return q.Cross.Live + q.Matrix.Live + q.Other.Live
}

func (q AdminJobQueue) Stale() int64 {
	return q.Cross.Stale + q.Matrix.Stale + q.Other.Stale
}

// AdminInsights is a bounded, read-only view for operator decisions.
type AdminInsights struct {
	WindowStart  time.Time
	WindowEnd    time.Time
	Daily        []AdminDailyStat
	Verification AdminVerificationCounts
	Ecosystems   []AdminEcosystemCount
	PackageDepth []AdminPackageDepth
	Search       AdminSearchOutcomeCounts
	Jobs         AdminJobQueue
}

var _ AdminInsightsReader = (*PG)(nil)

// AdminInsights performs bounded aggregate reads on indexed 30-day windows
// and the currently claimable/live verification-job set. stats_daily returns
// at most 31 rows (30 visible days plus one
// baseline); result and ecosystem groups are fixed buckets, and package
// depth is length-bounded and limited to ten rows.
func (p *PG) AdminInsights(ctx context.Context, now time.Time) (AdminInsights, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	windowStart := today.AddDate(0, 0, -(AdminInsightDays - 1))
	baselineStart := windowStart.AddDate(0, 0, -1)
	out := AdminInsights{
		WindowStart: windowStart,
		WindowEnd:   now,
	}

	err := p.withConn(ctx, func(conn *pgx.Conn) error {
		rows, err := conn.Query(ctx, `
			SELECT day, stats::text
			FROM stats_daily
			WHERE day >= $1::date AND day <= $2::date
			ORDER BY day ASC
			LIMIT 31`, baselineStart.Format("2006-01-02"), today.Format("2006-01-02"))
		if err != nil {
			return err
		}
		for rows.Next() {
			var day time.Time
			var raw string
			if err := rows.Scan(&day, &raw); err != nil {
				rows.Close()
				return err
			}
			out.Daily = append(out.Daily, decodeAdminDailyStat(day, raw))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = conn.Query(ctx, `
			WITH recent_results AS (
				SELECT CASE UPPER(COALESCE(contract_result, ''))
					WHEN 'PASS' THEN 'PASS'
					WHEN 'FAIL' THEN 'FAIL'
					WHEN 'SKIPPED' THEN 'SKIPPED'
					ELSE 'UNCLASSIFIED'
				END AS result_bucket
				FROM receipts
				WHERE created_at >= $1 AND created_at <= $2
			)
			SELECT result_bucket, COUNT(*)
			FROM recent_results
			GROUP BY result_bucket
			ORDER BY result_bucket`, windowStart, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var result string
			var count int64
			if err := rows.Scan(&result, &count); err != nil {
				rows.Close()
				return err
			}
			switch strings.ToUpper(result) {
			case "PASS":
				out.Verification.Pass += count
			case "FAIL":
				out.Verification.Fail += count
			case "SKIPPED":
				out.Verification.Skipped += count
			default:
				out.Verification.Unclassified += count
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = conn.Query(ctx, `
			WITH recent_pass_receipts AS (
				SELECT r.receipt_id,
				       CASE LOWER(r.receipt #>> '{environment,ecosystem}')
						WHEN 'npm' THEN 'npm'
						WHEN 'pypi' THEN 'pypi'
						WHEN 'cargo' THEN 'cargo'
						WHEN 'golang' THEN 'golang'
						WHEN 'gem' THEN 'gem'
						WHEN 'composer' THEN 'composer'
						WHEN 'pub' THEN 'pub'
						WHEN 'hex' THEN 'hex'
						WHEN 'maven' THEN 'maven'
						ELSE 'other'
				       END AS ecosystem
				FROM receipts r
				JOIN samples s ON s.sample_id = r.sample_id
				WHERE r.created_at >= $1 AND r.created_at <= $2
				  AND r.contract_result = 'PASS'
				  AND r.receipt #>> '{stages,contract}' = 'PASS'
				  AND NOT s.quarantined
			)
			SELECT ecosystem, COUNT(*)
			FROM recent_pass_receipts
			GROUP BY ecosystem
			ORDER BY COUNT(*) DESC, ecosystem ASC
			LIMIT 10`, windowStart, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var row AdminEcosystemCount
			if err := rows.Scan(&row.Ecosystem, &row.Verifications); err != nil {
				rows.Close()
				return err
			}
			out.Ecosystems = append(out.Ecosystems, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = conn.Query(ctx, `
			WITH recent_resolved AS (
				SELECT r.sample_id, r.receipt
				FROM receipts r
				JOIN samples s ON s.sample_id = r.sample_id
				WHERE r.created_at >= $1 AND r.created_at <= $2
				  AND r.contract_result = 'PASS'
				  AND r.receipt #>> '{stages,contract}' = 'PASS'
				  AND NOT s.quarantined
				  AND r.receipt ->> 'schemaVersion' = '2'
				  AND r.receipt #>> '{stages,resolve}' = 'PASS'
				  AND JSONB_TYPEOF(r.receipt -> 'resolvedPackages') = 'array'
			), package_refs AS (
				SELECT DISTINCT sample_id,
				       REGEXP_REPLACE(purl, '@[^@]*$', '') AS package_key
				FROM recent_resolved
				CROSS JOIN LATERAL JSONB_ARRAY_ELEMENTS_TEXT(
					receipt -> 'resolvedPackages'
				) AS purl
				WHERE OCTET_LENGTH(purl) <= 512
				  AND (purl LIKE 'pkg:npm/%@%'
				    OR purl LIKE 'pkg:pypi/%@%'
				    OR purl LIKE 'pkg:cargo/%@%'
				    OR purl LIKE 'pkg:golang/%@%'
				    OR purl LIKE 'pkg:gem/%@%'
				    OR purl LIKE 'pkg:composer/%@%'
				    OR purl LIKE 'pkg:pub/%@%'
				    OR purl LIKE 'pkg:hex/%@%'
				    OR purl LIKE 'pkg:maven/%@%')
			)
			SELECT package_key, COUNT(*) AS verified_samples
			FROM package_refs
			GROUP BY package_key
			ORDER BY verified_samples DESC, package_key ASC
			LIMIT $3`, windowStart, now, adminTopPackageLimit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var key string
			var count int64
			if err := rows.Scan(&key, &count); err != nil {
				rows.Close()
				return err
			}
			ecosystem, name, ok := decodeAdminPackageKey(key)
			if ok {
				out.PackageDepth = append(out.PackageDepth, AdminPackageDepth{
					Ecosystem: ecosystem, Name: name, VerifiedSamples: count,
				})
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		var firstDay, lastDay string
		if err := conn.QueryRow(ctx, `
			SELECT COUNT(*),
			       COALESCE(SUM(sample_hits), 0),
			       COALESCE(SUM(no_matches), 0),
			       COALESCE(MIN(day)::text, ''),
			       COALESCE(MAX(day)::text, '')
			FROM search_outcomes_daily
			WHERE day >= $1::date AND day <= $2::date`,
			windowStart.Format("2006-01-02"), today.Format("2006-01-02"),
		).Scan(&out.Search.Days, &out.Search.SampleHits, &out.Search.NoMatches, &firstDay, &lastDay); err != nil {
			return err
		}
		out.Search.FirstDay, out.Search.LastDay = firstDay, lastDay
		out.Search.Available = out.Search.Days > 0 && out.Search.Total() > 0

		cutoff := now.Add(-JobLease)
		var oldest *time.Time
		if err := conn.QueryRow(ctx, `
			SELECT
			  COUNT(*) FILTER (WHERE reason='cross' AND (status='open' OR (status='claimed' AND claimed_at < $1))),
			  COUNT(*) FILTER (WHERE reason='cross' AND status='claimed' AND claimed_at >= $1),
			  COUNT(*) FILTER (WHERE reason='cross' AND status='claimed' AND claimed_at < $1),
			  COUNT(*) FILTER (WHERE reason='matrix' AND (status='open' OR (status='claimed' AND claimed_at < $1))),
			  COUNT(*) FILTER (WHERE reason='matrix' AND status='claimed' AND claimed_at >= $1),
			  COUNT(*) FILTER (WHERE reason='matrix' AND status='claimed' AND claimed_at < $1),
			  COUNT(*) FILTER (WHERE reason NOT IN ('cross','matrix') AND (status='open' OR (status='claimed' AND claimed_at < $1))),
			  COUNT(*) FILTER (WHERE reason NOT IN ('cross','matrix') AND status='claimed' AND claimed_at >= $1),
			  COUNT(*) FILTER (WHERE reason NOT IN ('cross','matrix') AND status='claimed' AND claimed_at < $1),
			  COUNT(DISTINCT claimed_by) FILTER (
			    WHERE status='claimed' AND claimed_at >= $1 AND claimed_by IS NOT NULL AND claimed_by <> ''),
			  MIN(created_at) FILTER (WHERE status='open' OR (status='claimed' AND claimed_at < $1))
			FROM verification_jobs
			WHERE status IN ('open','claimed')`, cutoff).Scan(
			&out.Jobs.Cross.Claimable, &out.Jobs.Cross.Live, &out.Jobs.Cross.Stale,
			&out.Jobs.Matrix.Claimable, &out.Jobs.Matrix.Live, &out.Jobs.Matrix.Stale,
			&out.Jobs.Other.Claimable, &out.Jobs.Other.Live, &out.Jobs.Other.Stale,
			&out.Jobs.LiveClaimants, &oldest,
		); err != nil {
			return err
		}
		if oldest != nil {
			out.Jobs.OldestClaimable = oldest.UTC()
			out.Jobs.HasOldest = true
		}
		return nil
	})
	return out, err
}

type adminStatsDocument struct {
	Evidence        *int64 `json:"evidence"`
	VerifiedSamples *int64 `json:"verifiedSamples"`
	Packages        *int64 `json:"packages"`
}

func decodeAdminDailyStat(day time.Time, raw string) AdminDailyStat {
	row := AdminDailyStat{Day: day.UTC()}
	var doc adminStatsDocument
	if json.Unmarshal([]byte(raw), &doc) != nil {
		return row
	}
	row.Evidence = adminMetric(doc.Evidence)
	row.VerifiedSamples = adminMetric(doc.VerifiedSamples)
	row.Packages = adminMetric(doc.Packages)
	return row
}

func adminMetric(value *int64) AdminMetricValue {
	if value == nil || *value < 0 {
		return AdminMetricValue{}
	}
	return AdminMetricValue{Value: *value, Valid: true}
}

func decodeAdminPackageKey(key string) (ecosystem, name string, ok bool) {
	rest, ok := strings.CutPrefix(key, "pkg:")
	if !ok {
		return "", "", false
	}
	ecosystem, encoded, ok := strings.Cut(rest, "/")
	if !ok || ecosystem == "" || encoded == "" {
		return "", "", false
	}
	name, err := url.PathUnescape(encoded)
	if err != nil || name == "" {
		return "", "", false
	}
	return strings.ToLower(ecosystem), name, true
}
