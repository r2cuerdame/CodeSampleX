package serverstore

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

type failureIssueDependencyStore interface {
	IngestBatches(context.Context, []domain.ObservationBatch) (int, []RejectedBatch, error)
	FailureIssueDependencies(context.Context, string, string, string) ([]DependencyEdge, error)
}

// This runs against both stores below. The evidence bit belongs only to the
// child resolved by the same project/epoch that reported the exact failure;
// another failure or a PASS on the same parent release cannot promote it.
func assertFailureIssueDependencyEvidence(t *testing.T, store failureIssueDependencyStore) {
	t.Helper()
	const (
		parent = "pkg:npm/issue-parent@2.0.0"
		exact  = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		other  = "sha256:" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	env := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64"}
	rows := []domain.ObservationBatch{
		{SchemaVersion: 1, Epoch: "2026-09-06", AnonID: "peer-exact", ProjectBucket: "project-exact",
			Package: parent, Environment: env, Stage: domain.StageProjectTest, Result: domain.ResultFail,
			ErrorFingerprint: exact, ErrorCode: "ERR_EXACT", ObservationCount: 1,
			DependsOn: []string{"pkg:npm/exact-child@2.0.0"}},
		{SchemaVersion: 1, Epoch: "2026-09-06", AnonID: "peer-other", ProjectBucket: "project-other",
			Package: parent, Environment: env, Stage: domain.StageProjectTest, Result: domain.ResultFail,
			ErrorFingerprint: other, ErrorCode: "ERR_OTHER", ObservationCount: 1,
			DependsOn: []string{"pkg:npm/other-child@2.0.0"}},
		{SchemaVersion: 1, Epoch: "2026-09-06", AnonID: "peer-pass", ProjectBucket: "project-pass",
			Package: parent, Environment: env, Stage: domain.StageProjectTest, Result: domain.ResultPass,
			ObservationCount: 1, DependsOn: []string{"pkg:npm/pass-child@2.0.0"}},
	}
	accepted, rejected, err := store.IngestBatches(t.Context(), rows)
	if err != nil || accepted != len(rows) || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	edges, err := store.FailureIssueDependencies(t.Context(), "npm", "issue-parent", exact)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 3 {
		t.Fatalf("edges = %+v, want all three matrix rows", edges)
	}
	for _, edge := range edges {
		switch edge.ChildName {
		case "exact-child":
			if !edge.SameReceipt || edge.Outcome != "fail" {
				t.Errorf("exact edge = %+v, want same-receipt failure evidence", edge)
			}
		case "other-child", "pass-child":
			if edge.SameReceipt || edge.Outcome != "" {
				t.Errorf("unrelated edge = %+v, must remain a hypothesis", edge)
			}
		default:
			t.Errorf("unexpected edge %+v", edge)
		}
	}
}

func TestFailureIssueDependenciesUseExactSameReceiptInFake(t *testing.T) {
	assertFailureIssueDependencyEvidence(t, NewFake())
}

func TestIntegrationFailureIssueDependenciesUseExactSameReceipt(t *testing.T) {
	// openTestPG provides the repository's standard isolated PostgreSQL test
	// database and skips when that integration environment is unavailable.
	assertFailureIssueDependencyEvidence(t, openTestPG(t))
}
