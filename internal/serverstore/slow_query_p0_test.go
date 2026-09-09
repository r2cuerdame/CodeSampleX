package serverstore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationFailureClustersIndexScanEliminatesSort(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	var indexes []string
	_ = pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, _ := c.Query(ctx, `SELECT indexname FROM pg_indexes WHERE tablename='failure_clusters'`)
		defer rows.Close()
		for rows.Next() {
			var name string
			_ = rows.Scan(&name)
			indexes = append(indexes, name)
		}
		return nil
	})
	t.Logf("Indexes on failure_clusters: %v", indexes)

	const pkg = "github.com/jackc/pgx/v5"
	const clusterCount = 150

	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		// Insert clusters for other packages so failure_clusters has realistic cardinality
		// (a single package represents a small fraction of the total failure clusters).
		for other := 0; other < 100; other++ {
			otherPkg := fmt.Sprintf("other/pkg/%d", other)
			for j := 0; j < 50; j++ {
				if _, err := c.Exec(ctx, `
					INSERT INTO failure_clusters (
						ecosystem, package_name, symbol, stage, error_fp,
						error_code, observation_count, env_summary, hypotheses,
						regression_candidate, versions, evidence_quality
					) VALUES (
						'golang', $1, $2, 'build', $3,
						'E001', $4, '{"os":"linux"}'::jsonb, '[]'::jsonb,
						false, '["v1.0.0"]'::jsonb, 'complete'
					)`,
					otherPkg, fmt.Sprintf("sym-%d", j), fmt.Sprintf("fp-%d-%d", other, j), j,
				); err != nil {
					return err
				}
			}
		}

		for i := 0; i < 50; i++ {
			if _, err := c.Exec(ctx, `
				INSERT INTO failure_clusters (
					ecosystem, package_name, symbol, stage, error_fp,
					error_code, observation_count, env_summary, hypotheses,
					regression_candidate, versions, evidence_quality
				) VALUES (
					'golang', $1, $2, 'build', $3,
					'E001', $4, '{"os":"linux"}'::jsonb, '[]'::jsonb,
					false, '["v5.10.0"]'::jsonb, 'complete'
				)`,
				pkg, fmt.Sprintf("QueryRow-%d", i), fmt.Sprintf("fp-%04d", i), (i*37)%500+1,
			); err != nil {
				return err
			}
		}
		_, err := c.Exec(ctx, `ANALYZE failure_clusters`)
		return err
	})
	if err != nil {
		t.Fatalf("seed failure clusters: %v", err)
	}

	query := `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT id, COALESCE(ecosystem,''), COALESCE(package_name,''),
		       COALESCE(symbol,''), COALESCE(stage,''), COALESCE(error_fp,''),
		       COALESCE(error_code,''), COALESCE(observation_count,0),
		       COALESCE(env_summary::text,''), COALESCE(hypotheses::text,''),
		       COALESCE(regression_candidate,false), COALESCE(versions::text,''),
		       COALESCE(termination_kind,''), exit_code, COALESCE(signal,''),
		       COALESCE(timeout_millis,0), COALESCE(error_summary,''),
		       COALESCE(evidence_quality,'legacy-evidence-incomplete'),
		       COALESCE(env_variants::text,'[]'), COALESCE(evidence_breakdown::text,'{}'),
		       COALESCE(diagnostic_candidate,false),
		       COALESCE(outer_commands::text,'[]'), COALESCE(actual_toolchain,''),
		       COALESCE(stage_evidence,''), COALESCE(failure_evidence_gap,''),
		       first_seen, last_seen
		FROM failure_clusters
		WHERE package_name=$1
		  AND ` + CurrentFailureClusterPredicateSQL + `
		ORDER BY observation_count DESC, id`

	// 1. Measure plan WITH composite index (shipped by migration 0037).
	var planWithIndex strings.Builder
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, query, pkg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planWithIndex.WriteString(line + "\n")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain with index: %v", err)
	}

	planText := planWithIndex.String()
	t.Logf("Plan WITH failure_clusters_pkg_count_idx:\n%s", planText)

	// Verify that failure_clusters_pkg_count_idx enables a pure Index Scan with zero Sort
	// when sorting is not preferred (such as high cardinality or bounded memory).
	var planSortOff strings.Builder
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		_, _ = c.Exec(ctx, "SET enable_sort = off")
		rows, err := c.Query(ctx, query, pkg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planSortOff.WriteString(line + "\n")
		}
		_, _ = c.Exec(ctx, "SET enable_sort = on")
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain with enable_sort = off: %v", err)
	}
	sortOffText := planSortOff.String()
	t.Logf("Plan WITH failure_clusters_pkg_count_idx (enable_sort = off):\n%s", sortOffText)
	if !strings.Contains(sortOffText, "failure_clusters_pkg_count_idx") {
		t.Fatalf("expected pure index scan to use failure_clusters_pkg_count_idx, got:\n%s", sortOffText)
	}
	if strings.Contains(sortOffText, "Sort Method") || strings.Contains(sortOffText, "Incremental Sort") {
		t.Fatalf("expected no Sort node with failure_clusters_pkg_count_idx, got:\n%s", sortOffText)
	}

	// 2. Measure plan WITHOUT composite index to verify previous regression.
	var planWithoutIndex strings.Builder
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `DROP INDEX IF EXISTS failure_clusters_pkg_count_idx`); err != nil {
			return err
		}
		rows, err := c.Query(ctx, query, pkg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planWithoutIndex.WriteString(line + "\n")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain without index: %v", err)
	}

	planWithoutText := planWithoutIndex.String()
	t.Logf("Plan WITHOUT failure_clusters_pkg_count_idx (BEFORE fix):\n%s", planWithoutText)
	if !strings.Contains(planWithoutText, "Sort") {
		t.Fatalf("expected plan without composite index to contain Sort, got:\n%s", planWithoutText)
	}
}

func TestIntegrationSamplesPaginationIndexScanEliminatesIncrementalSort(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	var indexes []string
	_ = pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, _ := c.Query(ctx, `SELECT indexname FROM pg_indexes WHERE tablename='samples'`)
		defer rows.Close()
		for rows.Next() {
			var name string
			_ = rows.Scan(&name)
			indexes = append(indexes, name)
		}
		return nil
	})
	t.Logf("Indexes on samples: %v", indexes)

	const sampleCount = 500
	now := time.Now().UTC()

	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		// In production, samples are inserted in batches or CI sweeps sharing identical timestamps.
		for batch := 0; batch < 20; batch++ {
			batchTime := now.Add(-time.Duration(batch*10) * time.Minute)
			for j := 0; j < 50; j++ {
				idx := batch*50 + j + 1
				sampleID := fmt.Sprintf("sha256:%064x", idx)
				if _, err := c.Exec(ctx, `
					INSERT INTO samples (
						sample_id, manifest, status, license, size_bytes,
						hot_score, created_at, quarantined
					) VALUES (
						$1, '{"packages":["pkg:npm/foo@1.0.0"]}'::jsonb, 'PASS', 'MIT', 1024,
						0, $2, false
					)`,
					sampleID, batchTime,
				); err != nil {
					return err
				}
			}
		}
		_, err := c.Exec(ctx, `ANALYZE samples`)
		return err
	})
	if err != nil {
		t.Fatalf("seed samples: %v", err)
	}

	query := `EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT ` + sampleCols + `
		FROM samples
		WHERE NOT quarantined
		ORDER BY created_at DESC, sample_id
		LIMIT 50 OFFSET 0`

	// 1. Measure plan WITH composite index (shipped by migration 0037).
	var planWithIndex strings.Builder
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planWithIndex.WriteString(line + "\n")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain samples with index: %v", err)
	}

	planText := planWithIndex.String()
	t.Logf("Plan WITH samples_live_created_id_idx:\n%s", planText)

	if !strings.Contains(planText, "samples_live_created_id_idx") {
		t.Fatalf("expected query to use samples_live_created_id_idx, got:\n%s", planText)
	}
	if strings.Contains(planText, "Incremental Sort") || strings.Contains(planText, "Sort Method") {
		t.Fatalf("expected query plan WITH composite index to contain no Sort, got:\n%s", planText)
	}

	// 2. Measure plan WITHOUT composite index (with legacy samples_live_idx only).
	var planWithoutIndex strings.Builder
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `DROP INDEX IF EXISTS samples_live_created_id_idx`); err != nil {
			return err
		}
		rows, err := c.Query(ctx, query)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			planWithoutIndex.WriteString(line + "\n")
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain samples without index: %v", err)
	}

	planWithoutText := planWithoutIndex.String()
	t.Logf("Plan WITHOUT samples_live_created_id_idx (legacy samples_live_idx):\n%s", planWithoutText)
	if !strings.Contains(planWithoutText, "Incremental Sort") && !strings.Contains(planWithoutText, "Sort") {
		t.Fatalf("expected plan without composite index to contain Incremental Sort or Sort, got:\n%s", planWithoutText)
	}
}

func TestAuthoringExpansionCandidatesDemandBoundedLimit(t *testing.T) {
	if strings.Contains(authoringCoverageCTE, "LIMIT 5000") {
		t.Fatalf("authoringCoverageCTE dependency_open should not contain raw edge LIMIT 5000: demand ranking is preserved without early truncation")
	}
	if !strings.Contains(authoringExpansionCandidatesSQL, "LIMIT $3") {
		t.Fatalf("expected authoringExpansionCandidatesSQL to bound dependency_closure via LIMIT $3, got:\n%s", authoringExpansionCandidatesSQL)
	}
}
