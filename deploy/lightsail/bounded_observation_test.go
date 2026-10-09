package lightsail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

func observerPython(t *testing.T) string {
	t.Helper()
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "python3"}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Fatal("Python required for the shipped observer collector tests")
	return ""
}

func TestFailureLedgerBehavioralRegressions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, observerPython(t), "failure_ledger_test.py", "-v")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("collector behavioral regressions: %v: %s", err, output)
	}
}

type ledgerProof struct {
	Rows         int64  `json:"rows"`
	CurrentRows  int64  `json:"currentRows"`
	Observations int64  `json:"observations"`
	Unbalanced   int64  `json:"unbalanced"`
	Modern       int64  `json:"modern"`
	Pages        int64  `json:"pages"`
	MaxPageRows  int64  `json:"maxPageRows"`
	SourceRows   int64  `json:"sourceRows"`
	Fail         *int64 `json:"fail"`
	Detail       struct {
		Complete  bool  `json:"complete"`
		Fail      int64 `json:"fail"`
		Pass      int64 `json:"pass"`
		Published int64 `json:"publishedSamples"`
	} `json:"detail"`
}

// Execute the shipped Python walk and its exact SQL against one real,
// read-only repeatable-read PostgreSQL transaction. No Go copy of its paging
// algorithm or aggregate query can mask a collector regression.
func ledgerWalk(t *testing.T, conn *pgx.Conn, mode string, small bool, hook func(string)) (*ledgerProof, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var before string
	const identity = "SELECT pg_backend_pid()::text || ':' || txid_current_snapshot()::text || ':' || current_setting('transaction_read_only') || ':' || current_setting('transaction_isolation')"
	if err = tx.QueryRow(ctx, identity).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(before, ":on:repeatable read") {
		t.Fatalf("not a read-only stable snapshot: %s", before)
	}
	size := "normal"
	if small {
		size = "small"
	}
	cmd := exec.CommandContext(ctx, observerPython(t), "-u", "-c", ledgerBridgeDriver, "collect-failure-ledger.py", mode, size)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(output)
	var proof *ledgerProof
	status := "unavailable"
	for scanner.Scan() {
		var message struct {
			Query string       `json:"query"`
			Proof *ledgerProof `json:"proof"`
			Error string       `json:"error"`
		}
		if err = json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		if message.Query != "" {
			if hook != nil {
				hook(message.Query)
			}
			var result string
			err = tx.QueryRow(ctx, message.Query).Scan(&result)
			if err != nil {
				_, _ = io.WriteString(input, "{\"bridgeError\":true}\n")
			} else {
				_, _ = io.WriteString(input, result+"\n")
			}
			continue
		}
		if message.Proof != nil {
			proof = message.Proof
			status = "complete"
		} else if message.Error == "BudgetExceeded" {
			status = "budget-exceeded"
		}
	}
	_ = input.Close()
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatalf("shipped collector bridge: %v: %s", err, stderr.String())
	}
	if proof != nil {
		var after string
		if err = tx.QueryRow(ctx, identity).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if before != after {
			t.Fatal("coverage did not share a stable read-only snapshot")
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return proof, status
}

const ledgerBridgeDriver = `
import importlib.util,json,sys
s=importlib.util.spec_from_file_location("ledger",sys.argv[1]);m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
if sys.argv[3]=="small":
 m.PAGE_ROWS=2;m.SOURCE_ROWS=3;m.SOURCE_SQL=m.SOURCE_SQL.replace("10001","4")
 m.DETAIL_ROWS=4;m.DETAIL_SQL=m.DETAIL_SQL.replace("250001","5").replace("250000","4")
def query(sql):
 print(json.dumps({"query":sql}),flush=True)
 response=json.loads(sys.stdin.readline())
 if response.get("bridgeError"): raise m.Unmeasured("query unavailable")
 return response
try:
 result=m.walk(query,sys.argv[2]);print(json.dumps({"proof":result}),flush=True)
except m.Unmeasured as e:
 print(json.dumps({"error":type(e).__name__}),flush=True)

`

func TestObservationInvariantBudgetsPrecedeAggregates(t *testing.T) {
	helper := readDeployFixture(t, "collect-failure-ledger.py")
	for _, required := range []string{"PAGE_ROWS = 2000", "SQL_SECONDS = 20", "COMMAND_SECONDS = 30", "JSON_BYTES = 4096", "SOURCE_ROWS = 10000", "DETAIL_ROWS = 250000",
		"BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY", "self.deadline = self.started + SQL_SECONDS",
		"i.indisprimary AND i.indisvalid AND i.indisready", "i.indnkeyatts=1 AND a.amname='btree' AND p.attname='id'",
		"ORDER BY id LIMIT __PAGE_ROWS__", "cursor.replace", "maxPageRows", "AS MATERIALIZED",
		"pg_column_size(evidence_breakdown) <= 4096", "pg_column_compression(evidence_breakdown) IS NULL",
		"COALESCE(bool_and(within_json_budget),true)", "fc.observation_count::numeric <> breakdown.total",
		"LIMIT 10001", "FROM samples LIMIT 250001", "<= 250000", "total[\"currentRows\"] == 0", "session.finish()"} {
		if !strings.Contains(helper, required) {
			t.Errorf("collector omits existing budget/exhaustion proof %q", required)
		}
	}
	if strings.Contains(helper, "jsonb_each(") {
		t.Fatal("collector expands unbounded JSON")
	}
	for _, name := range []string{"collect-post-deploy-observation.sh", "collect-production-evidence.sh"} {
		if !strings.Contains(readDeployFixture(t, name), "csx_collect_failure_ledger") {
			t.Fatalf("%s omits the exhaustive collector", name)
		}
	}
	observer := readDeployFixture(t, "observe-production.ps1")
	for _, required := range []string{"$ledgerPrelude", "ReadAllText($ledgerCollector)", "$detailedSource = $ledgerPrelude", "snapshotComplete", "exhausted", "incomplete page coverage"} {
		if !strings.Contains(observer, required) {
			t.Errorf("observer omits source/proof %q", required)
		}
	}
	workflow := readDeployFixture(t, filepath.Join("..", "..", ".github", "workflows", "post-deploy-observation.yml"))
	if !strings.Contains(workflow, ":deploy/lightsail/collect-failure-ledger.py") || !strings.Contains(workflow, `cat "$ledger"`) {
		t.Fatal("supersession fails to pin and embed the same dependency")
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
		{"printf '%s\\n' 'budget-exceeded||||||||false|false'", "budget-exceeded||||0"},
		{"printf '%s\\n' 'complete|1|0||3|0|1|1|true|true'", "complete||3|0|0"},
	} {
		program := "set -eu\nPATH=/usr/bin:$PATH\ncsx_collect_failure_ledger() { " + fixture.docker + "; }\n" +
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
csx_collect_failure_ledger() { printf '%s\n' 'complete|1|0||3|0|1|1|true|true'; }
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
		{name: "cluster-sentinel-now-exhaustive", setup: `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,5)n`, status: "complete"},
		{name: "source-cap-exact", setup: `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'PASS',1 FROM generate_series(1,3)n`, status: "complete", wantFail: int64Pointer(0)},
		{name: "source-sentinel", setup: `INSERT INTO evidence_agg(id,result,observation_count) SELECT n,'PASS',1 FROM generate_series(1,4)n`, status: "budget-exceeded"},
		{name: "json-byte-budget", setup: `INSERT INTO failure_clusters VALUES(1,1,'complete','fp',jsonb_build_object('complete',1,'extra',repeat('x',5000)))`, status: "budget-exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, "TRUNCATE evidence_agg,failure_clusters;"+tc.setup); err != nil {
				t.Fatal(err)
			}
			proof, status := ledgerWalk(t, conn, "settled", true, nil)
			var sources int64
			var fail, observations, invalid *int64
			if proof != nil {
				sources = proof.SourceRows
				fail = proof.Fail
				observations = &proof.Observations
				invalid = &proof.Unbalanced
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
	proof, status := ledgerWalk(t, conn, "settled", true, nil)
	if status != "complete" || proof.SourceRows != 0 {
		t.Fatal("nonempty ledger evaluated unnecessary source work")
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
		{"cluster-now-exhaustive", `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,5)n`},
		{"samples-over-budget", `INSERT INTO samples SELECT n::text,'PUBLISHED' FROM generate_series(1,5)n`},
		{"json-over-budget", `INSERT INTO failure_clusters VALUES(1,1,'complete','fp',jsonb_build_object('complete',1,'extra',repeat('x',5000)))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := conn.Exec(ctx, "TRUNCATE evidence_agg,failure_clusters,samples;"+tc.setup); err != nil {
				t.Fatal(err)
			}
			proof, status := ledgerWalk(t, conn, "extended", true, nil)
			if strings.Contains(tc.name, "over-budget") {
				if status != "budget-exceeded" || proof != nil {
					t.Fatalf("partial census published: %s %#v", status, proof)
				}
			} else {
				if status != "complete" || proof == nil || !proof.Detail.Complete {
					t.Fatalf("exact census unavailable: %s", status)
				}
				want := int64(0)
				if tc.name == "complete" {
					want = 3
				}
				if proof.Detail.Fail != want {
					t.Fatalf("exact FAIL=%d want %d", proof.Detail.Fail, want)
				}
				if tc.name == "cluster-now-exhaustive" && proof.Rows != 5 {
					t.Fatal("cluster tail was not measured")
				}
			}
		})
	}
}

func TestIntegrationFailureLedgerExhaustsActualLargeCorpusAndDetectsTail(t *testing.T) {
	conn := observationTestPG(t)
	ctx := context.Background()
	if _, err := conn.Exec(ctx, `INSERT INTO failure_clusters SELECT n,1,'complete','fp','{"complete":1}' FROM generate_series(1,270003)n`); err != nil {
		t.Fatal(err)
	}
	var oldComplete bool
	if err := conn.QueryRow(ctx, `WITH prefix AS MATERIALIZED(SELECT id FROM failure_clusters LIMIT 250001) SELECT count(*)<=250000 FROM prefix`).Scan(&oldComplete); err != nil {
		t.Fatal(err)
	}
	if oldComplete {
		t.Fatal("negative control failed to reproduce the old sentinel rejection")
	}
	proof, status := ledgerWalk(t, conn, "settled", false, nil)
	if status != "complete" || proof == nil || proof.Rows != 270003 || proof.Observations != 270003 || proof.Unbalanced != 0 || proof.Pages != 136 || proof.MaxPageRows != 2000 {
		t.Fatalf("full corpus coverage: %s %#v", status, proof)
	}
	if _, err := conn.Exec(ctx, `UPDATE failure_clusters SET evidence_breakdown='{"complete":0}' WHERE id=270003`); err != nil {
		t.Fatal(err)
	}
	proof, status = ledgerWalk(t, conn, "settled", false, nil)
	if status != "complete" || proof == nil || proof.Rows != 270003 || proof.Unbalanced != 1 {
		t.Fatalf("invalid tail beyond the old cap was lost: %s %#v", status, proof)
	}
}

func TestIntegrationFailureLedgerRequiresExistingPrimaryKey(t *testing.T) {
	conn := observationTestPG(t)
	if _, err := conn.Exec(context.Background(), "ALTER TABLE failure_clusters DROP CONSTRAINT failure_clusters_pkey"); err != nil {
		t.Fatal(err)
	}
	proof, status := ledgerWalk(t, conn, "settled", true, func(sql string) {
		if strings.Contains(sql, "FROM failure_clusters") {
			t.Fatal("missing index started a corpus scan")
		}
	})
	if status != "unavailable" || proof != nil {
		t.Fatal("missing index published evidence")
	}
}

func TestIntegrationFailureLedgerPagesShareOneSnapshotDuringWrites(t *testing.T) {
	conn := observationTestPG(t)
	ctx := context.Background()
	// These are isolated CI test fixtures, never production schema changes.
	schema := pgx.Identifier{"observer_snapshot_" + strconv.FormatInt(time.Now().UnixNano(), 10)}.Sanitize()
	_, err := conn.Exec(ctx, "DROP TABLE pg_temp.evidence_agg,pg_temp.failure_clusters,pg_temp.samples;CREATE SCHEMA "+schema+";SET search_path="+schema+`;CREATE TABLE evidence_agg(id bigint PRIMARY KEY,result text,observation_count bigint,purl text,symbol text,evidence_quality text);
CREATE TABLE failure_clusters(id bigint PRIMARY KEY,observation_count bigint,evidence_quality text,error_fp text,evidence_breakdown jsonb);
CREATE TABLE samples(sample_id text PRIMARY KEY,status text);
INSERT INTO failure_clusters VALUES(1,1,'complete','fp','{"complete":1}'),(2,1,'complete','fp','{"complete":1}');`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "SET search_path=public;DROP SCHEMA "+schema+" CASCADE")
	})
	writer, err := pgx.Connect(ctx, os.Getenv("CSX_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(ctx)
	if _, err = writer.Exec(ctx, "SET search_path="+schema); err != nil {
		t.Fatal(err)
	}
	pages := 0
	proof, status := ledgerWalk(t, conn, "settled", true, func(sql string) {
		if strings.Contains(sql, "FROM failure_clusters") {
			pages++
			if pages == 2 {
				if _, err = writer.Exec(ctx, `INSERT INTO failure_clusters VALUES(3,1,'complete','fp','{"complete":1}')`); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
	if status != "complete" || proof == nil || proof.Rows != 2 || proof.Pages != 2 {
		t.Fatalf("pages mixed concurrent snapshots: %s %#v", status, proof)
	}
	proof, status = ledgerWalk(t, conn, "settled", true, nil)
	if status != "complete" || proof == nil || proof.Rows != 3 {
		t.Fatal("subsequent snapshot did not include the committed row")
	}
}
