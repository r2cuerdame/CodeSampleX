package serverstore

// The dependency-axis query runs on every builder pass, on a two-core host
// that also serves the website. So the thing to hold is not its wall clock on
// a laptop -- which proves nothing about that host -- but that its cost scales
// with the BACKLOG rather than with the corpus.
//
// The first version ranked by demand with a GROUP BY over the whole of
// evidence_agg. That is the corpus's largest relation and reading it whole is
// the ~700MB scan that starved the candidate query for a week (#173), spent
// here every pass to order a few dozen rows. The shipped version reads demand
// per candidate through evidence_agg_target_idx instead.
//
// This is the guard the memory says to use: a plan shape, not a stopwatch. A
// sequential scan of evidence_agg is the same defect on any machine.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// scansRelationSequentially reports whether any node in the plan reads the
// named relation with a Seq Scan.
func scansRelationSequentially(n planNode, relation string) bool {
	if n.RelationName == relation && n.NodeType == "Seq Scan" {
		return true
	}
	for _, child := range n.Plans {
		if scansRelationSequentially(child, relation) {
			return true
		}
	}
	return false
}

func TestIntegrationDependencyAxisQueryDoesNotReadTheWholeEvidenceTable(t *testing.T) {
	pg := openTestPG(t) // skips when CSX_TEST_DSN is unset
	ctx := context.Background()

	// Production's proportions in miniature: evidence is large relative to
	// the open backlog, which is the whole reason ranking must not read it
	// whole. Statistics are collected, because a planner without them hashes
	// small tables and the test would pass on the very SQL that is wrong.
	const names, evidencePerName = 200, 6
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		for n := 0; n < names; n++ {
			name := fmt.Sprintf("axis-%03d", n)
			purl := "pkg:npm/" + name + "@1.0.0"
			if _, err := c.Exec(ctx, `
				INSERT INTO packages(purl, ecosystem, name, version, major, publicness)
				VALUES ($1, 'npm', $2, '1.0.0', '1', 'PUBLIC')`, purl, name); err != nil {
				return err
			}
			for s := 0; s < evidencePerName; s++ {
				if _, err := c.Exec(ctx, `
					INSERT INTO evidence_agg(purl, symbol, env_hash, stage, result, observation_count,
					                         unique_peer_buckets, unique_project_buckets, env_json, direct)
					VALUES ($1, $2, 'env-hash', 'PROJECT_COMPILE', 'PASS', 3, 1, 1,
					        '{"os":"linux"}'::jsonb, false)
					ON CONFLICT DO NOTHING`, purl, fmt.Sprintf("sym-%d", s)); err != nil {
					return err
				}
			}
			// Only a few of them are open dependency-axis work: a sample with
			// a passing receipt and nothing that answered the tree.
			if n%25 != 0 {
				continue
			}
			sampleID := "sha256:axis-" + name
			if _, err := c.Exec(ctx, `
				INSERT INTO samples(sample_id, manifest, status, license, size_bytes, created_at)
				VALUES ($1, $2::jsonb, 'CROSS_PASS', 'MIT-0', 1, now())`,
				sampleID, `{"packages":["`+purl+`"],"symbols":[]}`); err != nil {
				return err
			}
			if _, err := c.Exec(ctx, `
				INSERT INTO sample_packages(sample_id, purl, coord)
				VALUES ($1, $2, $3)`, sampleID, purl, "pkg:npm/"+name+"@"); err != nil {
				return err
			}
			if _, err := c.Exec(ctx, `
				INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, contract_result, receipt, created_at)
				VALUES ($1, $2, 'peer-axis', $3, 'PASS', $4::jsonb, now())`,
				"r-axis-"+name, sampleID, "env-"+name, `{"environment":{"os":"linux"}}`); err != nil {
				return err
			}
		}
		_, err := c.Exec(ctx, `ANALYZE packages; ANALYZE evidence_agg; ANALYZE sample_packages; ANALYZE samples; ANALYZE receipts`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var raw string
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `SET max_parallel_workers_per_gather = 0`); err != nil {
			return err
		}
		return c.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+dependencyAxisOpenSQL,
			DependencyAxisMaxAttempts, DependencyAxisPerPass*8).Scan(&raw)
	})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var top []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) == 0 {
		t.Fatalf("unexpected EXPLAIN shape: %v\n%s", err, raw)
	}
	if scansRelationSequentially(top[0].Plan, "evidence_agg") {
		t.Fatalf("the dependency-axis query reads evidence_agg sequentially; on production that is "+
			"a whole-corpus scan every builder pass, spent to order a few dozen rows:\n%s", raw)
	}
}
