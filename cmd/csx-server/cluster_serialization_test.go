package main

import (
	"context"
	"fmt"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"testing"
	"time"
)

func TestFarm202CompleteCurrentLedgerSerializationBudget(t *testing.T) {
	rows := make([]serverstore.ClusterRow, 600)
	for i := range rows {
		rows[i] = serverstore.ClusterRow{Ecosystem: "npm", PackageName: "many", Symbol: fmt.Sprintf("call%d", i), Stage: "PROJECT_TEST", ErrorFingerprint: "sha256:failure", ObservationCount: int64(i + 1)}
	}
	w := &webStore{s: &completeFailureClusterStore{Fake: serverstore.NewFake(), rows: rows}}
	ctx := context.Background()
	n := testing.AllocsPerRun(15, func() {
		docs, err := w.FailureIssueClusters(ctx, "npm", "many")
		if err != nil || len(docs) != 600 {
			t.Fatal("complete current ledger lost")
		}
	})
	t.Logf("current 600-row complete ledger serialization allocations: %.0f", n)
	if n > 12000 {
		t.Fatalf("complete ledger repeatedly sorted and marshalled per-row maps: %.0f allocations, budget 12000", n)
	}
}
func TestFarm202FailureClusterDocumentPreservesExactBytes(t *testing.T) {
	exit := 17
	first := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	c := serverstore.ClusterRow{ActualToolchain: "go1.26", ObservationCount: 7, DiagnosticCandidate: true, EnvSummaryJSON: `{"b":2,"a":1}`, EnvVariantsJSON: `[{"os":"linux"}]`, ErrorCode: "E_TEST", ErrorSummary: "quoted \" & < >", EvidenceBreakdownJSON: `{"PASS":2}`, FailureEvidenceGap: "none", EvidenceQuality: "full", ExitCode: &exit, ErrorFingerprint: "sha256:failure", FirstSeen: first, HypothesesJSON: `[{"domain":"runtime","confidence":"possible"}]`, LastSeen: first.Add(time.Minute), OuterCommands: []string{"go test", "quoted \" & < >"}, RegressionCandidate: true, Signal: "SIGTERM", Stage: "PROJECT_TEST", StageEvidence: "evidence", Symbol: "pkg.Call", TerminationKind: "exit", TimeoutMillis: 180000, VersionsJSON: `["v1","v2"]`}
	got, ok := failureClusterJSON(c)
	const expected = "{\"actualToolchain\":\"go1.26\",\"count\":7,\"diagnosticCandidate\":true,\"envSummary\":{\"b\":2,\"a\":1},\"envVariants\":[{\"os\":\"linux\"}],\"errorCode\":\"E_TEST\",\"errorSummary\":\"quoted \\\" \\u0026 \\u003c \\u003e\",\"evidenceBreakdown\":{\"PASS\":2},\"evidenceGap\":\"none\",\"evidenceQuality\":\"full\",\"exitCode\":17,\"fingerprint\":\"sha256:failure\",\"firstSeen\":\"2026-10-09T10:00:00Z\",\"hypotheses\":[{\"domain\":\"runtime\",\"confidence\":\"possible\"}],\"lastSeen\":\"2026-10-09T10:01:00Z\",\"outerCommands\":[\"go test\",\"quoted \\\" \\u0026 \\u003c \\u003e\"],\"regressionCandidate\":true,\"signal\":\"SIGTERM\",\"stage\":\"PROJECT_TEST\",\"stageEvidence\":\"evidence\",\"symbol\":\"pkg.Call\",\"terminationKind\":\"exit\",\"timeoutMillis\":180000,\"versions\":[\"v1\",\"v2\"]}"
	if !ok || got != expected {
		t.Fatalf("failure evidence byte contract changed: got %s", got)
	}
	c.EnvSummaryJSON = "{broken"
	if _, ok := failureClusterJSON(c); ok {
		t.Fatal("malformed raw JSON must still be rejected")
	}
}
