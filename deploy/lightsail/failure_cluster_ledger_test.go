package lightsail

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The deploy transaction refuses to commit when the cluster-observation
// ledger moves, and it computes that ledger in shell, not in Go. So the
// server and the gate can disagree silently: after migration 0024 preserved
// the pre-contract rows, the shell summed every historical row while the
// server served only the current ones, and the gate read a doubling that the
// site never showed.
//
// There is one predicate, and it lives in Go. Both scripts must spell it the
// same way, whitespace aside.
func TestDeployLedgerUsesTheServersOwnCurrentClusterPredicate(t *testing.T) {
	want := normalizeSQL(strings.TrimSuffix(strings.TrimPrefix(
		strings.TrimSpace(serverstore.CurrentFailureClusterPredicateSQL), "("), ")"))
	if want == "" {
		t.Fatal("the shared predicate is empty")
	}
	for _, name := range []string{"deploy.ps1", "collect-production-evidence.sh"} {
		script := normalizeSQL(readDeployFixture(t, name))
		if !strings.Contains(script, want) {
			t.Errorf("%s does not compute the ledger with the server's predicate\nwant: %s", name, want)
		}
		if strings.Contains(script, normalizeSQL(`(SELECT COALESCE(SUM(observation_count),0) FROM failure_clusters)`)) {
			t.Errorf("%s still sums every historical and current failure-cluster row", name)
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
