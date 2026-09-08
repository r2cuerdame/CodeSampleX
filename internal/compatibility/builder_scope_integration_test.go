package compatibility

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func openBuilderTestPG(t *testing.T) (*serverstore.PG, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		require := os.Getenv("CSX_REQUIRE_TEST_DSN")
		if off, err := strconv.ParseBool(require); require != "" && (err != nil || !off) {
			t.Fatal("CSX_REQUIRE_TEST_DSN requires real PostgreSQL")
		}
		t.Skip("CSX_TEST_DSN is unset")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("csx_builder_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		conn.Close(context.Background())
	})
	if _, err := conn.Exec(ctx, "SET search_path="+schema); err != nil {
		t.Fatal(err)
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pg, err := serverstore.Open(ctx, dsn+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version/10000 != 17 {
		t.Fatalf("requires PostgreSQL17, got %d: %v", version, err)
	}
	return pg, conn
}

func builderSQL(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func builderDocumentState(t *testing.T, conn *pgx.Conn) map[string]string {
	t.Helper()
	out := map[string]string{}
	// Preserve every semantic field. Only the public materialization timestamp
	// differs when a full repair rewrites an otherwise untouched document.
	queries := []string{
		"SELECT purl || ':' || symbol, (snapshot - 'generatedAt')::text FROM compatibility_snapshots",
		"SELECT key, (json - 'generatedAt')::text FROM shards",
		"SELECT ecosystem || '/' || package_name || ':' || symbol || ':' || stage || ':' || error_fp, (to_jsonb(failure_clusters) - 'id' - 'first_seen' - 'last_seen')::text FROM failure_clusters",
		"SELECT sample_id || ':' || reason || ':' || want_env::text, status FROM verification_jobs",
	}
	for i, q := range queries {
		rows, err := conn.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			out[fmt.Sprintf("%d:%s", i, key)] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	return out
}

func TestIntegrationBuilderScopedOutputParityAndFailure(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	seedBuilderFixture(t, pg)
	save := func(id string, packages []string, symbol string) {
		manifest := domain.SampleManifest{SchemaVersion: 1, Packages: packages, Symbols: []string{symbol}, Environment: envNode("esm"), License: "MIT-0"}
		if err := pg.SaveSample(ctx, serverstore.SampleRow{SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(manifest)), Status: "CROSS_PASS"}); err != nil {
			t.Fatal(err)
		}
	}
	receipt := func(id, sample string, packages []string, verdict string) {
		rec := domain.VerificationReceipt{SchemaVersion: 2, SampleID: sample, CaseID: sample, ResolvedPackages: packages, PeerID: "peer-" + id, Environment: envNode("esm"), Stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": verdict}, VerifierAdapter: "node-typescript@1", SandboxCapability: domain.CapContainerRun}
		if err := pg.SaveReceipt(ctx, serverstore.ReceiptRow{ReceiptID: id, SampleID: sample, PeerID: rec.PeerID, EnvHash: rec.Environment.Hash(), ContractResult: verdict, ReceiptJSON: string(domain.MustCanonicalJSON(rec))}); err != nil {
			t.Fatal(err)
		}
	}
	save("undeclared", []string{"pkg:npm/declared@1.0.0"}, "actual.call")
	receipt("actual-old", "undeclared", []string{"pkg:npm/actual@1.0.0"}, "PASS")
	save("wide", []string{"pkg:npm/owner@1.0.0", "pkg:npm/side@1.0.0"}, "shared")
	receipt("wide-old", "wide", []string{"pkg:npm/owner@1.0.0", "pkg:npm/side@1.0.0"}, "PASS")
	save("narrow", []string{"pkg:npm/owner@1.0.0"}, "shared")
	receipt("narrow-old", "narrow", []string{"pkg:npm/owner@1.0.0"}, "PASS")
	builderSQL(t, conn, "UPDATE samples SET created_at=$1, updated_at=$1", now.Add(-time.Hour))
	builderSQL(t, conn, "UPDATE receipts SET created_at=$1", now.Add(-time.Hour))
	builderSQL(t, conn, "UPDATE evidence_agg SET last_seen=$1, first_seen=$1", now.Add(-time.Hour))
	b := &Builder{Store: pg, Now: func() time.Time { return now }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertParity := func(label string) {
		t.Helper()
		var phaseLogs []string
		b.phaseLogf = func(format string, args ...any) { phaseLogs = append(phaseLogs, fmt.Sprintf(format, args...)) }
		if err := b.RunOnce(ctx); err != nil {
			t.Fatalf("%s incremental: %v", label, err)
		}
		got := builderDocumentState(t, conn)
		reference := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-time.Hour), passes: fullPassEvery}
		if err := reference.RunOnce(ctx); err != nil {
			t.Fatalf("%s full reference: %v", label, err)
		}
		want := builderDocumentState(t, conn)
		if !reflect.DeepEqual(got, want) {
			for key, value := range want {
				if got[key] != value {
					t.Errorf("%s key=%s incremental=%s full=%s", label, key, got[key], value)
				}
			}
			for key := range got {
				if _, ok := want[key]; !ok {
					t.Errorf("%s extra incremental key %s", label, key)
				}
			}
			t.FailNow()
		}
		for _, line := range phaseLogs {
			if strings.Contains(line, "event=exit") && (strings.Contains(line, "sample_page_read") || strings.Contains(line, "receipt_page_read") || strings.Contains(line, "scope_claim_read") || strings.Contains(line, "list_targets")) {
				t.Log(line)
			}
		}
	}
	now = now.Add(10 * time.Minute)
	receipt("actual-new", "undeclared", []string{"pkg:npm/actual@2.0.0"}, "FAIL")
	builderSQL(t, conn, "UPDATE receipts SET created_at=$1 WHERE receipt_id='actual-new'", now.Add(-time.Minute))
	assertParity("undeclared resolved cross-major")
	if js, ok, err := pg.GetSnapshot(ctx, "pkg:npm/actual@2.0.0", "actual.call"); err != nil || !ok || !strings.Contains(js, "FAIL") {
		t.Fatalf("exact receipt evidence absent: %s %v", js, err)
	}
	now = now.Add(10 * time.Minute)
	builderSQL(t, conn, "UPDATE samples SET quarantined=true, updated_at=$1 WHERE sample_id='narrow'", now.Add(-time.Minute))
	assertParity("quarantine restores global competitor")
	if _, ok, err := pg.GetSnapshot(ctx, "pkg:npm/side@1.0.0", "shared"); err != nil || !ok {
		t.Fatalf("global symbol not restored: %v", err)
	}
	now = now.Add(10 * time.Minute)
	builderSQL(t, conn, "UPDATE samples SET quarantined=true, updated_at=$1 WHERE sample_id='undeclared'", now.Add(-time.Minute))
	assertParity("retire all resolved majors")
	if _, ok, err := pg.GetSnapshot(ctx, "pkg:npm/actual@2.0.0", "actual.call"); err != nil || ok {
		t.Fatalf("retired snapshot still serves: %v", err)
	}
	before := builderDocumentState(t, conn)
	marker, passes := b.lastRun, b.passes
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := b.RunOnce(cancelled); err == nil {
		t.Fatal("cancelled pass succeeded")
	}
	if b.lastRun != marker || b.passes != passes {
		t.Fatal("cancel advanced completion marker")
	}
	if after := builderDocumentState(t, conn); !reflect.DeepEqual(before, after) {
		t.Fatal("cancel changed materializations")
	}
	now = now.Add(10 * time.Minute)
	save("ambiguous", []string{"pkg:npm/%61lias@1.0.0"}, "alias")
	if err := b.RunOnce(ctx); err == nil || !strings.Contains(err.Error(), "ambiguous legacy") {
		t.Fatalf("unsafe scope must fail closed: %v", err)
	}
	if b.lastRun != marker || b.passes != passes {
		t.Fatal("failed scope advanced completion marker")
	}
	if after := builderDocumentState(t, conn); !reflect.DeepEqual(before, after) {
		t.Fatal("failed scope changed materializations")
	}
}
