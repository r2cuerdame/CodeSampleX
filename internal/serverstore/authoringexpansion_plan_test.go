package serverstore

// The expansion query must not visit the package table once per failure
// cluster.
//
// Measured on production 2026-09-02 (#173): ListAuthoringExpansionCandidates
// took 83s, then 211s under farm load, against its own 10s statement
// timeout. The plan put the cost in one place. The FINDING branch expands
// each cluster's `versions` array with a LATERAL function scan, and the
// planner answered the join to `packages` by rebuilding a tiny hash from an
// index scan for EVERY expanded row:
//
//	Hash Join  (actual time=0.520..0.526 rows=1 loops=159601)
//	  -> Function Scan on jsonb_array_elements_text  (loops=159599)
//	  -> Hash (rows=4 loops=159601)
//	       -> Index Scan using packages_name_idx on packages p  (loops=159601)
//
// 0.5ms x 159,601 = 84s of the 101s that branch cost. `packages` has 4,307
// rows. Hashing it once and streaming the expanded clusters through is a
// fraction of a second; hashing four of its rows 160k times is the whole
// outage.
//
// This is the guard the memory says to use: a loop count from EXPLAIN, not a
// wall clock. Wall clocks vary by machine and by load and pass on a fast
// laptop; a plan that scans `packages` 300 times for 300 clusters is the
// same defect at any speed.
//
// It runs only against PostgreSQL (CSX_TEST_DSN), because the plan is the
// thing under test and the Fake has no plan.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// planNode is the slice of EXPLAIN (FORMAT JSON) this test reads.
type planNode struct {
	NodeType     string     `json:"Node Type"`
	RelationName string     `json:"Relation Name"`
	ActualLoops  int        `json:"Actual Loops"`
	Plans        []planNode `json:"Plans"`
}

// maxLoopsOnRelation walks a plan and returns the highest Actual Loops of any
// node that scans the named relation, and the node type that had it.
func maxLoopsOnRelation(n planNode, relation string) (int, string) {
	best, kind := 0, ""
	if n.RelationName == relation && n.ActualLoops > best {
		best, kind = n.ActualLoops, n.NodeType
	}
	for _, child := range n.Plans {
		if l, k := maxLoopsOnRelation(child, relation); l > best {
			best, kind = l, k
		}
	}
	return best, kind
}

func TestIntegrationExpansionQueryDoesNotScanPackagesPerCluster(t *testing.T) {
	pg := openTestPG(t) // skips when CSX_TEST_DSN is unset
	ctx := context.Background()

	// The shape that makes the planner choose the per-row path, taken from
	// production's proportions: several versions per package name and many
	// clusters per name, with statistics collected. A first version of this
	// test seeded one package per cluster and never ran ANALYZE, and the
	// planner hashed the whole table -- the test passed on the very SQL that
	// was down in production. A guard that passes on the defect is not one.
	//
	// Every cluster is current under CurrentFailureClusterPredicateSQL by
	// carrying evidence_quality 'complete'; error_fp is distinct per row
	// because (ecosystem, package_name, symbol, stage, error_fp) is unique.
	const names, versions, clustersPerName = 60, 4, 40
	const clusters = names * clustersPerName
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		for n := 0; n < names; n++ {
			name := fmt.Sprintf("pkg-%03d", n)
			for v := 0; v < versions; v++ {
				version := fmt.Sprintf("1.%d.0", v)
				if _, err := c.Exec(ctx, `
					INSERT INTO packages(purl, ecosystem, name, version, major, publicness)
					VALUES ($1, 'npm', $2, $3, '1', 'PUBLIC')`,
					"pkg:npm/"+name+"@"+version, name, version); err != nil {
					return err
				}
			}
			for k := 0; k < clustersPerName; k++ {
				if _, err := c.Exec(ctx, `
					INSERT INTO failure_clusters(ecosystem, package_name, symbol, stage, error_fp,
					                             observation_count, env_summary, versions, evidence_quality)
					VALUES ('npm', $1, $2, 'build', $3, 3, '{"os":"linux"}'::jsonb, $4::jsonb, 'complete')`,
					name, fmt.Sprintf("sym-%d", k%7), fmt.Sprintf("fp-%d", k), fmt.Sprintf(`["1.%d.0"]`, k%versions)); err != nil {
					return err
				}
			}
		}
		_, err := c.Exec(ctx, `ANALYZE packages; ANALYZE failure_clusters`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var raw string
	err = pg.withConn(ctx, func(c *pgx.Conn) error {
		// One worker, so Actual Loops is a count and not a per-worker share.
		if _, err := c.Exec(ctx, `SET max_parallel_workers_per_gather = 0`); err != nil {
			return err
		}
		return c.QueryRow(ctx,
			`EXPLAIN (ANALYZE, FORMAT JSON) `+authoringExpansionCandidatesSQL,
			200, authoringSiblingVersionsPerPackage, authoringDependencyClosureCap, authoringResolveWeight,
			domain.DependencyScannableEcosystems(),
		).Scan(&raw)
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

	// The query reads `packages` from several branches; each is allowed a
	// handful of passes, never one per cluster. Anything approaching the
	// cluster count is the nested loop production died on.
	const allowed = 8
	loops, kind := maxLoopsOnRelation(top[0].Plan, "packages")
	if loops > allowed {
		t.Fatalf("a %s on packages ran %d times for %d clusters (allowed %d): "+
			"the join is being answered once per expanded cluster row, which is "+
			"0.5ms x 160k = 84s on production", kind, loops, clusters, allowed)
	}
}
