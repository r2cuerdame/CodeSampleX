package lightsail

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Observation must use the server's current cluster predicate so preserved
// legacy rows cannot be mistaken for a current ledger correctness failure.
func TestObservationLedgerUsesTheServersOwnCurrentClusterPredicate(t *testing.T) {
	want := normalizeSQL(strings.TrimSuffix(strings.TrimPrefix(
		strings.TrimSpace(serverstore.CurrentFailureClusterPredicateSQL), "("), ")"))
	if want == "" {
		t.Fatal("the shared predicate is empty")
	}
	for _, name := range []string{"collect-production-evidence.sh"} {
		script := normalizeSQL(readDeployFixture(t, name))
		if !strings.Contains(script, want) {
			t.Errorf("%s does not compute the ledger with the server's predicate\nwant: %s", name, want)
		}
		if strings.Contains(script, normalizeSQL(`(SELECT COALESCE(SUM(observation_count),0) FROM failure_clusters)`)) {
			t.Errorf("%s still sums every historical and current failure-cluster row", name)
		}
	}
}

// Detailed derived-ledger checks remain available after activation, but cannot
// hold the rollback transaction open or turn builder convergence into rollback.
func TestDetailedLedgerChecksBelongToObservation(t *testing.T) {
	collector := readDeployFixture(t, "collect-production-evidence.sh")
	for _, required := range []string{
		`jsonb_each(fc.evidence_breakdown)`,
		`item.key NOT IN ('complete','partial','missing','legacy-evidence-incomplete')`,
		`fc.observation_count::numeric <> COALESCE`,
		`'unbalancedFailureClusterRows'`,
		`server_started_at=$(docker inspect codesamplex-server-1`,
		`builder_generated_at=$(docker compose exec -T db psql`,
		`builder_fresh=true`,
		`printf 'builder_fresh=%s\n' "$builder_fresh"`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("observation evidence omits derived-ledger detail %q", required)
		}
	}
	observer := readDeployFixture(t, "observe-production.ps1")
	if !strings.Contains(observer, "collect-production-evidence.sh") {
		t.Error("post-deploy observation no longer collects detailed invariants")
	}
}

func TestDeploymentTransactionNeverScansDetailedInvariants(t *testing.T) {
	for _, name := range []string{"deploy.ps1", "deploy-production.ps1", "collect-deploy-identity.sh"} {
		script := readDeployFixture(t, name)
		for _, forbidden := range []string{
			"collectInvariantScript", "sourceInvariantIndexes", "jsonb_each(fc.evidence_breakdown)",
			"collect-production-evidence.sh", "builderFreshPoll", "collectBuilderFreshScript",
			"complete + partial + missing + legacy-evidence-incomplete does not equal FAIL",
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s still performs expensive observation inside deployment: %q", name, forbidden)
			}
		}
	}
}

// The verifier image and the deploy bundle read the same migration file, so
// the destructive statement that caused the doubling must stay out of it.
func TestFailureEvidenceMigrationStaysAdditive(t *testing.T) {
	sql := readDeployFixture(t, filepath.Join("..", "..", "internal", "serverstore",
		"migrations", "0024_failure_evidence.sql"))
	for _, forbidden := range []string{"TRUNCATE", "DROP ", "DELETE FROM"} {
		if strings.Contains(strings.ToUpper(sql), forbidden) {
			t.Errorf("0024 contains %q — clearing derived production data is a separately authorized lifecycle", forbidden)
		}
	}
}

// normalizeSQL collapses every run of whitespace to one space so a predicate
// wrapped across shell lines still compares equal to its Go source.
func normalizeSQL(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
