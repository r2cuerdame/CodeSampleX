package lightsail

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func observerSQLBetween(t *testing.T, source, start, end string) string {
	t.Helper()
	i := strings.Index(source, start)
	if i < 0 {
		t.Fatalf("missing query start %q", start)
	}
	source = source[i+len(start):]
	j := strings.Index(source, end)
	if j < 0 {
		t.Fatalf("missing query end %q", end)
	}
	return source[:j]
}

func settledObserverSQL(t *testing.T) string {
	return observerSQLBetween(t, readDeployFixture(t, "collect-post-deploy-observation.sh"),
		`if settled_invariant=$(docker compose exec -T db psql -U csx -d csx -At -F '|' -c "`, `" 2>/dev/null); then`)
}

func extendedObserverSQL(t *testing.T, field string) string {
	source := readDeployFixture(t, "collect-production-evidence.sh")
	scope := observerSQLBetween(t, source, "bounded_ledger_scope=\"", ")\"") + ")"
	return scope + observerSQLBetween(t, source,
		field+`=$(docker compose exec -T db psql -U csx -d csx -Atqc "$bounded_ledger_scope`, `")`)
}

func TestObservationInvariantBudgetsPrecedeAggregates(t *testing.T) {
	for _, name := range []string{"collect-post-deploy-observation.sh", "collect-production-evidence.sh"} {
		script := readDeployFixture(t, name)
		for _, required := range []string{"AS MATERIALIZED", "FROM failure_clusters LIMIT 250001", "<= 250000",
			"pg_column_size(evidence_breakdown) <= 4096", "pg_column_compression(evidence_breakdown) IS NULL",
			"COALESCE(bool_and(within_json_budget),true)", "fc.observation_count::numeric <> breakdown.total"} {
			if !strings.Contains(script, required) {
				t.Errorf("%s omits budget/proof %q", name, required)
			}
		}
		if strings.Contains(script, "jsonb_each(") {
			t.Errorf("%s expands an unbounded JSON object", name)
		}
	}
	settled := settledObserverSQL(t)
	if strings.Count(settled, "FROM failure_clusters") != 1 || strings.Count(settled, "FROM evidence_agg") != 1 ||
		!strings.Contains(settled, "current_rows = 0 FROM cluster_totals") || !strings.Contains(settled, "LIMIT 10001") {
		t.Fatal("settled proof must scan the capped ledger once and read source only for an empty ledger")
	}
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	if !strings.Contains(collector, "statement_timeout=20000") || !strings.Contains(collector, "settled_fail_observations=\n") {
		t.Fatal("SQL budget or unmeasured count representation changed")
	}
	for _, field := range []string{"invariants", "failure_evidence_quality"} {
		sql := extendedObserverSQL(t, field)
		if !strings.Contains(sql, "FROM bounded_evidence") || !strings.Contains(sql, "collection-budget-exceeded") ||
			!strings.Contains(sql, "FROM samples LIMIT 250001") {
			t.Errorf("extended %s can represent an incomplete census as exact", field)
		}
		if strings.Contains(strings.ToUpper(sql), "ORDER BY") {
			t.Errorf("extended %s permits an unbounded sort before its cap", field)
		}
	}
	if strings.Contains(strings.ToUpper(settled), "ORDER BY") {
		t.Fatal("settled proof permits an unbounded sort before its cap")
	}
	modern := observerSQLBetween(t, readDeployFixture(t, "collect-production-evidence.sh"),
		`modern_failure_clusters=$(docker compose exec -T db psql -U csx -d csx -Atqc "`, `")`)
	if strings.Contains(strings.ToUpper(modern), "ORDER BY") || !strings.Contains(modern, "FROM failure_clusters LIMIT 250001") {
		t.Fatal("modern cluster detail permits unbounded input work")
	}
}

func TestObservationSettledQueryFailurePreservesUnavailableEvidence(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	source := readDeployFixture(t, "collect-post-deploy-observation.sh")
	block := "settled_invariant_status=unavailable\n" + observerSQLBetween(t, source,
		"  settled_invariant_status=unavailable\n", "\nfi\n\nprintf 'revision=")
	for _, fixture := range []struct{ docker, want string }{
		{"return 1", "unavailable||||1"},
		{"return 124", "unavailable||||124"},
		{"printf '%s\\n' 'budget-exceeded|250001|0|||'", "budget-exceeded||||0"},
		{"printf '%s\\n' 'complete|1|0||3|0'", "complete||3|0|0"},
	} {
		program := "set -eu\nPATH=/usr/bin:$PATH\ndocker() { " + fixture.docker + "; }\n" +
			"settled_fail_observations=\nsettled_failure_cluster_observations=\nsettled_unbalanced_failure_cluster_rows=\n" + block +
			"\nprintf '%s|%s|%s|%s|%s' \"$settled_invariant_status\" \"$settled_fail_observations\" \"$settled_failure_cluster_observations\" \"$settled_unbalanced_failure_cluster_rows\" \"$settled_invariant_exit_code\"\n"
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, sh, "-s")
		cmd.Stdin = strings.NewReader(program)
		output, err := cmd.CombinedOutput()
		cancel()
		if err != nil || string(output) != fixture.want {
			t.Fatalf("collector failure/sentinel transport: err=%v output=%q want=%q", err, output, fixture.want)
		}
	}
}

func TestObservationTerminalLogAndEventFailuresAreNotMeasuredZeros(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil && runtime.GOOS == "windows" {
		sh, err = exec.LookPath(`C:\Program Files\Git\bin\sh.exe`)
	}
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	collector := readDeployFixture(t, "collect-post-deploy-observation.sh")
	terminal := "summarize_pressure() {" + observerSQLBetween(t, collector,
		"summarize_pressure() {", "\nprintf 'revision=")
	for _, failure := range []string{"none", "stderr", "logs", "oom", "restart", "die"} {
		t.Run(failure, func(t *testing.T) {
			program := "set -eu\nPATH=/usr/bin:$PATH\nfailed_probe=" + failure + `
container=codesamplex-server-1
observe_since=2026-09-12T00:00:00Z
include_detail=1
docker() {
  case "$1" in
    logs)
      if [ "$failed_probe" = logs ]; then return 124; fi
      if [ "$failed_probe" = stderr ]; then
        printf '%s\n' 'csx-server: db pressure path=/v1/wanted waited=2.5s pool_busy=1 query_timeout=1 pool_busy_total=7 query_timeout_total=3 admission_refused_total=2 deferred_refused_total=1' >&2
      fi ;;
    events) case "$*" in *"event=$failed_probe"*) return 124 ;; esac ;;
    compose) printf '%s\n' 'complete|1|0||3|0' ;;
    *) return 99 ;;
  esac
}
` + terminal + `
printf '%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s' "$detail_collected" "$pressure_lines" "$oom_events" "$restart_events" "$die_events" "$settled_invariant_status" "$pool_busy_events" "$query_timeout_events" "$pool_busy_event_total" "$query_timeout_event_total" "$admission_refused_event_total" "$deferred_refused_event_total" "$max_pressure_wait_seconds"
`
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, sh, "-s")
			cmd.Stdin = strings.NewReader(program)
			output, err := cmd.CombinedOutput()
			want := "false|0|0|0|0|complete|0|0|0|0|0|0|0.000000"
			if failure == "none" {
				want = "true|0|0|0|0|complete|0|0|0|0|0|0|0.000000"
			} else if failure == "stderr" {
				want = "true|1|0|0|0|complete|1|1|7|3|2|1|2.500000"
			}
			if err != nil || string(output) != want {
				t.Fatalf("terminal %s result err=%v output=%q want=%q", failure, err, output, want)
			}
		})
	}
}

func observationTestPG(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		require := os.Getenv("CSX_REQUIRE_TEST_DSN")
		if off, err := strconv.ParseBool(require); require != "" && (err != nil || off) {
			t.Fatal("CSX_REQUIRE_TEST_DSN requires CSX_TEST_DSN for observer SQL regression")
		}
		t.Skip("CSX_TEST_DSN unset; PostgreSQL observer SQL regression unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	_, err = conn.Exec(ctx, `
CREATE TEMP TABLE evidence_agg(id bigint PRIMARY KEY, result text, observation_count bigint, purl text, symbol text, evidence_quality text);
CREATE TEMP TABLE failure_clusters(id bigint PRIMARY KEY, observation_count bigint, evidence_quality text, error_fp text, evidence_breakdown jsonb);
CREATE TEMP TABLE samples(sample_id text PRIMARY KEY, status text);
SET statement_timeout = '5s';`)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestIntegrationSettledObservationPreservesInvariantTruth(t *testing.T) {
	conn := observationTestPG(t)
	ctx := context.Background()
	sql := strings.NewReplacer("250001", "5", "250000", "4", "10001", "4", "10000", "3").Replace(settledObserverSQL(t))
	for _, tc := range []struct {
		name, setup, status string
		wantInvalid         int64
		wantFail            *int64
	}{
		{name: "empty", status: "complete", wantFail: int64Pointer(0)},
		{name: "missing-materialization", setup: `INSERT INTO evidence_agg(id,result,observation_count) VALUES(1,'FAIL',7)`, status: "complete", wantFail: int64Pointer(7)},
		{name: "balanced-source-not-needed", setup: `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'FAIL',7 FROM generate_series(1,8)n; INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":4,"partial":3}')`, status: "complete"},
		{name: "wrong-total", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":6}')`, status: "complete", wantInvalid: 1},
		{name: "negative-value", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":8,"partial":-1}')`, status: "complete", wantInvalid: 1},
		{name: "string-value", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":"7"}')`, status: "complete", wantInvalid: 1},
		{name: "null-value", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":7,"partial":null}')`, status: "complete", wantInvalid: 1},
		{name: "malformed-array", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','[]')`, status: "complete", wantInvalid: 1},
		{name: "malformed-scalar", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','null')`, status: "complete", wantInvalid: 1},
		{name: "unknown-key", setup: `INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":7,"unknown":0}')`, status: "complete", wantInvalid: 1},
		{name: "zero-row", setup: `INSERT INTO failure_clusters VALUES(1,0,'complete','fp','{}')`, status: "complete", wantInvalid: 1},
		{name: "historical-row-excluded", setup: `INSERT INTO failure_clusters VALUES(1,7,'legacy-evidence-incomplete','old-fp','{}')`, status: "complete", wantFail: int64Pointer(0)},
		{name: "collapsed-legacy-row-current", setup: `INSERT INTO failure_clusters VALUES(1,7,'legacy-evidence-incomplete','','{"legacy-evidence-incomplete":7}')`, status: "complete"},
		{name: "cluster-cap-exact", setup: `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,4)n`, status: "complete"},
		{name: "cluster-sentinel", setup: `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,5)n`, status: "budget-exceeded"},
		{name: "source-cap-exact", setup: `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'PASS',1 FROM generate_series(1,3)n`, status: "complete", wantFail: int64Pointer(0)},
		{name: "source-sentinel", setup: `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'PASS',1 FROM generate_series(1,4)n`, status: "budget-exceeded"},
		{name: "json-byte-budget", setup: `INSERT INTO failure_clusters VALUES(1,1,'complete','fp',jsonb_build_object('complete',1,'extra',repeat('x',5000)))`, status: "budget-exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, "TRUNCATE evidence_agg,failure_clusters;"+tc.setup); err != nil {
				t.Fatal(err)
			}
			var status string
			var clusters, sources int64
			var fail, observations, invalid *int64
			if err := conn.QueryRow(ctx, sql).Scan(&status, &clusters, &sources, &fail, &observations, &invalid); err != nil {
				t.Fatal(err)
			}
			if status != tc.status || (fail == nil) != (tc.wantFail == nil) || (fail != nil && *fail != *tc.wantFail) {
				t.Fatalf("status=%s fail=%v, want status=%s fail=%v", status, fail, tc.status, tc.wantFail)
			}
			if status == "complete" && (invalid == nil || *invalid != tc.wantInvalid) {
				t.Fatalf("unbalanced rows=%v, want %d", invalid, tc.wantInvalid)
			}
			if status == "complete" && !strings.HasPrefix(tc.name, "malformed-") {
				var oldAccepted bool
				if err := conn.QueryRow(ctx, oldSettledAcceptanceSQL).Scan(&oldAccepted); err != nil {
					t.Fatal(err)
				}
				accepted := *invalid == 0 && (*observations > 0 || (fail != nil && *fail == 0))
				if accepted != oldAccepted {
					t.Fatalf("new acceptance %t differs from original invariant %t", accepted, oldAccepted)
				}
			}
			if observations != nil && *observations > 0 && sources != 0 {
				t.Fatal("nonempty ledger unnecessarily read source corpus")
			}
		})
	}
	// A source view that raises on any evaluated row establishes that zero
	// source rows means no corpus read, not a scan whose results were discarded.
	if _, err := conn.Exec(ctx, `TRUNCATE evidence_agg,failure_clusters;
INSERT INTO evidence_agg(id,result,observation_count) VALUES(1,'FAIL',7);
INSERT INTO failure_clusters VALUES(1,7,'complete','fp','{"complete":7}');
ALTER TABLE evidence_agg RENAME TO source_fixture;
CREATE TEMP VIEW evidence_agg AS SELECT id,result,observation_count/(id-id) AS observation_count FROM source_fixture;`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("nonempty ledger evaluated unnecessary source work: %v", rows.Err())
	}
}

func int64Pointer(v int64) *int64 { return &v }

// The prior production invariant is the oracle on fully measured, valid JSON
// fixtures. The new proof may omit source totals but cannot alter its decision.
const oldSettledAcceptanceSQL = `
WITH current_clusters AS (
 SELECT * FROM failure_clusters WHERE COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete') OR COALESCE(error_fp,'')=''
)
SELECT NOT (
 ((SELECT COALESCE(SUM(observation_count),0) FROM evidence_agg WHERE result='FAIL') > 0
  AND (SELECT COALESCE(SUM(observation_count),0) FROM current_clusters) <= 0)
 OR EXISTS (SELECT 1 FROM current_clusters fc WHERE fc.observation_count <= 0
  OR EXISTS (SELECT 1 FROM jsonb_each(fc.evidence_breakdown) item(key,value)
    WHERE item.key NOT IN ('complete','partial','missing','legacy-evidence-incomplete')
      OR jsonb_typeof(item.value) <> 'number'
      OR CASE WHEN jsonb_typeof(item.value)='number' THEN (item.value::text)::numeric < 0 ELSE false END)
  OR fc.observation_count::numeric <> COALESCE((SELECT SUM((item.value::text)::numeric)
    FROM jsonb_each(fc.evidence_breakdown) item(key,value) WHERE jsonb_typeof(item.value)='number'),0)))`

func TestIntegrationExtendedObservationOnlyPublishesExactCensuses(t *testing.T) {
	conn := observationTestPG(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, setup string }{
		{"empty", ""},
		{"complete", `INSERT INTO evidence_agg(id,result,observation_count,evidence_quality) VALUES(1,'FAIL',3,'complete'); INSERT INTO failure_clusters VALUES(1,3,'complete','fp','{"complete":3}'); INSERT INTO samples VALUES('sample','PUBLISHED');`},
		{"source-over-budget", `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'PASS',1 FROM generate_series(1,5)n`},
		{"cluster-over-budget", `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,5)n`},
		{"samples-over-budget", `INSERT INTO samples SELECT n::text,'PUBLISHED' FROM generate_series(1,5)n`},
		{"json-over-budget", `INSERT INTO failure_clusters VALUES(1,1,'complete','fp',jsonb_build_object('complete',1,'extra',repeat('x',5000)))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, "TRUNCATE evidence_agg,failure_clusters,samples;"+tc.setup); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"invariants", "failure_evidence_quality"} {
				sql := strings.NewReplacer("250001", "5", "250000", "4").Replace(extendedObserverSQL(t, field))
				var result string
				if err := conn.QueryRow(ctx, sql).Scan(&result); err != nil {
					t.Fatalf("%s: %v", field, err)
				}
				if strings.Contains(tc.name, "over-budget") {
					if result != "collection-budget-exceeded" {
						t.Fatalf("%s claimed a partial census: %s", field, result)
					}
				} else {
					var counts map[string]any
					if err := json.Unmarshal([]byte(result), &counts); err != nil {
						t.Fatal(err)
					}
					want := float64(0)
					if tc.name == "complete" {
						want = 3
					}
					if counts["fail"] != want {
						t.Fatalf("%s exact FAIL count=%v, want %v", field, counts["fail"], want)
					}
				}
			}
		})
	}
}
