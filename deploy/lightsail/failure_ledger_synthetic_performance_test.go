package lightsail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const syntheticLedgerRows int64 = 270003

type syntheticLedgerMetric struct {
	Shape        string       `json:"shape"`
	Status       string       `json:"status"`
	ElapsedMS    int64        `json:"elapsedMs"`
	SQLCalls     int          `json:"sqlCalls"`
	SameSnapshot bool         `json:"sameSnapshot"`
	Proof        *ledgerProof `json:"proof,omitempty"`
	FirstPageSQL string       `json:"-"`
}

// This probe is opt-in and accepts only the existing job-scoped CI database.
// It never copies production rows, disables vacuum, flushes caches, or changes
// the shipped collector's budgets. Go supplies the same 20s cumulative bound
// while executing the shipped Python walk and SQL in one read-only snapshot.
func TestIntegrationFailureLedgerSyntheticPerformance(t *testing.T) {
	if os.Getenv("CSX_SYNTHETIC_LEDGER_PERF") != "1" {
		t.Skip("opt-in disposable PostgreSQL performance experiment")
	}
	u, err := url.Parse(os.Getenv("CSX_TEST_DSN"))
	if err != nil || u.User == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") ||
		u.User.Username() != "csx" || u.Path != "/csx" || os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Fatal("synthetic experiment requires the existing localhost CI database")
	}
	password, _ := u.User.Password()
	if password != "csx-ci-throwaway" {
		t.Fatal("synthetic experiment refuses a non-CI database")
	}
	dsn := os.Getenv("CSX_TEST_DSN")
	setup, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal("cannot connect to disposable CI database")
	}
	defer setup.Close(context.Background())
	schema := pgx.Identifier{"observer_synthetic_" + strconv.FormatInt(time.Now().UnixNano(), 10)}.Sanitize()
	if _, err = setup.Exec(context.Background(), "CREATE SCHEMA "+schema+"; SET search_path="+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = setup.Exec(cleanup, "SET search_path=public; DROP SCHEMA "+schema+" CASCADE")
	}()
	var md5Parts []string
	for i := 0; i < 32; i++ {
		md5Parts = append(md5Parts, fmt.Sprintf("md5(n::text || ':synthetic_%02d')", i))
	}
	wide := strings.Join(md5Parts, " || ")
	for _, shape := range []string{"narrow", "wide-1024", "wide-1024-updated"} {
		buildCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		_, err = setup.Exec(buildCtx, `DROP TABLE IF EXISTS failure_clusters,evidence_agg,samples;
CREATE TABLE failure_clusters(id bigint PRIMARY KEY,observation_count bigint,evidence_quality text,error_fp text,evidence_breakdown jsonb,error_summary text,env_summary jsonb);
CREATE TABLE evidence_agg(id bigint PRIMARY KEY,result text,observation_count bigint,purl text,symbol text,evidence_quality text);
CREATE TABLE samples(sample_id text PRIMARY KEY,status text);`)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		padding := "''"
		if shape != "narrow" {
			padding = wide
		}
		insert := "INSERT INTO failure_clusters SELECT n,4,CASE WHEN n%3=0 THEN 'complete' ELSE 'legacy-evidence-incomplete' END,'synthetic-fp-'||n::text,'{\"complete\":1,\"partial\":1,\"missing\":1,\"legacy-evidence-incomplete\":1}'::jsonb," + padding + ",jsonb_build_object('fixture',md5(n::text)) FROM generate_series(1," + strconv.FormatInt(syntheticLedgerRows, 10) + ")n"
		if _, err = setup.Exec(buildCtx, insert); err != nil {
			cancel()
			t.Fatal(err)
		}
		if shape == "wide-1024-updated" {
			for round := 0; round < 3; round++ {
				if _, err = setup.Exec(buildCtx, `UPDATE failure_clusters SET observation_count=observation_count+1,evidence_breakdown=jsonb_build_object('complete',observation_count+1-3,'partial',1,'missing',1,'legacy-evidence-incomplete',1),error_summary=reverse(error_summary) WHERE id%4=0`); err != nil {
					cancel()
					t.Fatal(err)
				}
			}
		}
		// Fixture planner statistics only, never a production ANALYZE or cache reset.
		if _, err = setup.Exec(buildCtx, "ANALYZE failure_clusters"); err != nil {
			cancel()
			t.Fatal(err)
		}
		var heapBytes, totalBytes, meanRowBytes int64
		err = setup.QueryRow(buildCtx, `SELECT pg_relation_size('failure_clusters'),pg_total_relation_size('failure_clusters'),(SELECT avg(pg_column_size(f))::bigint FROM (SELECT * FROM failure_clusters ORDER BY id LIMIT 128)f)`).Scan(&heapBytes, &totalBytes, &meanRowBytes)
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		metric := syntheticLedgerWalk(t, dsn, schema, shape)
		expectedObs := 4 * (syntheticLedgerRows / 3)
		if shape == "wide-1024-updated" {
			expectedObs += 3 * (syntheticLedgerRows / 12)
		}
		syntheticCheckProof(t, metric, expectedObs, 0)
		canonical := syntheticCanonicalSession(t, schema, shape)
		syntheticCheckProof(t, canonical, expectedObs, 0)
		t.Logf("SYNTHETIC_CANONICAL_SESSION %s", syntheticJSON(t, canonical))
		t.Logf("SYNTHETIC_LEDGER %s", syntheticJSON(t, map[string]any{"metric": metric, "heapBytes": heapBytes, "totalBytes": totalBytes, "meanRowBytes": meanRowBytes, "expectedObservations": expectedObs, "cacheReset": false, "scope": "synthetic narrow/wide/update shapes; not a claimed clone of production churn or cache pressure"}))
		if metric.FirstPageSQL != "" {
			profile := syntheticFixturePagePlan(t, dsn, schema, metric.FirstPageSQL)
			t.Logf("SYNTHETIC_PAGE_PLAN %s", syntheticJSON(t, map[string]any{"shape": shape, "profile": profile, "scope": "one synthetic page after the walk, not cold production profiling"}))
		}
		if shape == "wide-1024-updated" && metric.Status == "complete" {
			if _, err = setup.Exec(context.Background(), "UPDATE failure_clusters SET evidence_breakdown='{\"complete\":0}' WHERE id="+strconv.FormatInt(syntheticLedgerRows, 10)); err != nil {
				t.Fatal(err)
			}
			tail := syntheticLedgerWalk(t, dsn, schema, shape+"-invalid-tail")
			syntheticCheckProof(t, tail, expectedObs, 1)
			t.Logf("SYNTHETIC_LEDGER %s", syntheticJSON(t, tail))
		}
	}
}

func syntheticJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func syntheticCheckProof(t *testing.T, m syntheticLedgerMetric, observations, unbalanced int64) {
	t.Helper()
	if m.Status != "complete" {
		if m.Proof != nil {
			t.Fatal("an incomplete synthetic walk published totals")
		}
		return
	}
	if !m.SameSnapshot || m.Proof == nil || m.Proof.Rows != syntheticLedgerRows ||
		m.Proof.CurrentRows != syntheticLedgerRows/3 || m.Proof.Observations != observations ||
		m.Proof.Unbalanced != unbalanced || m.Proof.Pages != 136 || m.Proof.MaxPageRows != 2000 {
		t.Fatalf("complete synthetic proof differs: %s", syntheticJSON(t, m))
	}
}

func syntheticLedgerWalk(t *testing.T, dsn, schema, shape string) syntheticLedgerMetric {
	t.Helper()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	metric := syntheticLedgerMetric{Shape: shape, Status: "unavailable"}
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("cannot connect synthetic reader")
	}
	defer conn.Close(context.Background())
	if _, err = conn.Exec(ctx, "SET search_path="+schema); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = tx.Rollback(cleanup)
	}()
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='20s'; SET LOCAL idle_in_transaction_session_timeout='20s'"); err != nil {
		t.Fatal(err)
	}
	identity := "SELECT pg_backend_pid()::text||':'||txid_current_snapshot()::text||':'||current_setting('transaction_read_only')||':'||current_setting('transaction_isolation')"
	var before string
	if err = tx.QueryRow(ctx, identity).Scan(&before); err != nil || !strings.HasSuffix(before, ":on:repeatable read") {
		t.Fatal("synthetic read-only snapshot unavailable")
	}
	cmd := exec.CommandContext(ctx, observerPython(t), "-u", "-c", ledgerBridgeDriver, "collect-failure-ledger.py", "settled", "normal")
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
	scanner.Buffer(make([]byte, 8192), 65536)
	var proof *ledgerProof
	var queryErr error
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
			metric.SQLCalls++
			if metric.FirstPageSQL == "" && strings.Contains(message.Query, "WITH cluster_rows AS MATERIALIZED") {
				metric.FirstPageSQL = message.Query
			}
			var result string
			err = tx.QueryRow(ctx, message.Query).Scan(&result)
			if err != nil {
				queryErr = err
				_, _ = io.WriteString(input, "{\"bridgeError\":true}\n")
			} else {
				_, _ = io.WriteString(input, result+"\n")
			}
		} else if message.Proof != nil {
			proof = message.Proof
		} else if message.Error == "BudgetExceeded" {
			metric.Status = "budget-exceeded"
		}
	}
	_ = input.Close()
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		metric.Status = "deadline"
	} else if scanner.Err() != nil || waitErr != nil {
		t.Fatalf("synthetic bridge failed: %v %v %s", scanner.Err(), waitErr, stderr.String())
	}
	if queryErr != nil && ctx.Err() == nil {
		t.Fatalf("synthetic SQL rejected: %v", queryErr)
	}
	if proof != nil && ctx.Err() == nil && waitErr == nil {
		var after string
		if err = tx.QueryRow(ctx, identity).Scan(&after); err == nil && after == before {
			if err = tx.Commit(ctx); err == nil {
				metric.Status = "complete"
				metric.SameSnapshot = true
				metric.Proof = proof
			}
		}
	}
	metric.ElapsedMS = time.Since(started).Milliseconds()
	return metric
}

func syntheticFixturePagePlan(t *testing.T, dsn, schema, sql string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("synthetic plan connection unavailable")
	}
	defer conn.Close(context.Background())
	if _, err = conn.Exec(ctx, "SET search_path="+schema); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SET LOCAL statement_timeout='20s'"); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+sql).Scan(&raw); err != nil {
		return map[string]any{"status": "bounded-plan-unavailable"}
	}
	var plan []map[string]any
	if err = json.Unmarshal(raw, &plan); err != nil || len(plan) != 1 {
		t.Fatal("synthetic plan malformed")
	}
	root, _ := plan[0]["Plan"].(map[string]any)
	return map[string]any{"status": "complete", "planningMs": plan[0]["Planning Time"], "executionMs": plan[0]["Execution Time"], "sharedHit": root["Shared Hit Blocks"], "sharedRead": root["Shared Read Blocks"], "tempRead": root["Temp Read Blocks"], "tempWritten": root["Temp Written Blocks"]}
}

const syntheticCanonicalSessionDriver = `
import importlib.util,json,subprocess,sys,time
spec=importlib.util.spec_from_file_location("ledger",sys.argv[1])
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)
assert (m.SQL_SECONDS,m.COMMAND_SECONDS,m.PAGE_ROWS,m.JSON_BYTES,m.SOURCE_ROWS,m.DETAIL_ROWS)==(20,30,2000,4096,10000,250000)
container,schema,docker=sys.argv[2:]
real_popen=subprocess.Popen
def disposable_popen(argv,*args,**kwargs):
 if argv[:5]!=["docker","compose","exec","-T","-e"] or argv[6]!="db" or argv[7]!="psql":
  raise RuntimeError("unexpected collector command")
 options=argv[5]+" -c search_path="+schema
 return real_popen([docker,"exec","-i","-e",options,container]+argv[7:],*args,**kwargs)
m.subprocess.Popen=disposable_popen
calls=0
original_query=m.Session.query
def counted_query(self,sql):
 global calls
 calls+=1
 return original_query(self,sql)
m.Session.query=counted_query
started=time.monotonic()
result={"status":"unavailable","sameSnapshot":False,"sqlCalls":0}
try:
 proof=m.collect("settled")
 result.update(status="complete",sameSnapshot=True,proof=proof)
except m.BudgetExceeded:
 result["status"]="budget-exceeded"
except (m.Unmeasured,OSError,ValueError,subprocess.SubprocessError) as e:
 result["failureType"]=type(e).__name__
result.update(elapsedMs=round((time.monotonic()-started)*1000),sqlCalls=calls)
print(json.dumps(result,separators=(",",":")))
`

// Reuse the existing job's PostgreSQL container, not another service. The only
// adapter replaces compose's container lookup with docker exec of that same
// container and adds the isolated synthetic schema to PGOPTIONS. The shipped
// Session, psql protocol, JSON/page limits, wall deadline and finish proof run
// unchanged. Compose bootstrap and production VM/cache conditions remain gaps.
func syntheticCanonicalSession(t *testing.T, schema, shape string) syntheticLedgerMetric {
	t.Helper()
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Fatal("canonical session probe requires disposable CI")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("existing CI Docker unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inspect := exec.CommandContext(ctx, docker, "ps", "--filter", "ancestor=postgres:17-alpine", "--format", "{{.ID}}")
	raw, err := inspect.Output()
	if err != nil {
		t.Fatal("existing CI PostgreSQL container lookup failed")
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 {
		t.Fatal("expected exactly one existing CI PostgreSQL container")
	}
	for _, r := range ids[0] {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatal("unexpected container identity")
		}
	}
	schemaName := strings.Trim(schema, "\"")
	if !strings.HasPrefix(schemaName, "observer_synthetic_") {
		t.Fatal("unexpected synthetic schema")
	}
	for _, r := range strings.TrimPrefix(schemaName, "observer_synthetic_") {
		if r < '0' || r > '9' {
			t.Fatal("unexpected synthetic schema suffix")
		}
	}
	cmd := exec.CommandContext(ctx, observerPython(t), "-u", "-c", syntheticCanonicalSessionDriver, "collect-failure-ledger.py", ids[0], schemaName, docker)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("canonical fixture driver failed: %v %s", err, stderr.String())
	}
	var metric syntheticLedgerMetric
	if err = json.Unmarshal(output, &metric); err != nil {
		t.Fatal("canonical fixture driver output malformed")
	}
	metric.Shape = shape + "-canonical-psql"
	if metric.Status == "unavailable" {
		t.Fatalf("canonical fixture Session unavailable: %s", output)
	}
	return metric
}
