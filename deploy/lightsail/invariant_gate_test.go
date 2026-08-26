package lightsail

import (
	"strings"
	"testing"
)

// What the deploy transaction may and may not conclude from the six numbers
// it reads on either side of a rollout.
//
// Five of them are ledgers the network appends to: PASS, FAIL, published
// samples and the two named pgx ParseConfig totals. A deploy that lowers one
// of those took evidence away, and the transaction must not commit.
//
// failureClusterObservations is not one of them. failure_clusters is
// materialized from that evidence, and the builder's first pass after any
// restart is a full one — so every deploy rebuilds it by construction. On
// 2026-08-26 that rebuild corrected a double count and moved 20098 to 20096
// while PASS and FAIL did not move at all, and the monotonic reading of it
// rolled a healthy deploy back. The question a materialization has to answer
// is whether it still accounts for its source, which is the coverage check
// this test pins.
func TestDeployGateSeparatesAppendedLedgersFromRebuiltClusters(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")

	for _, required := range []string{
		// The ledgers are named, so a failure says which number moved.
		`$ledgerInvariants = @(0, 1, 2, 4, 5)`,
		`throw "production ledger $($invariantNames[$i]) decreased: $($beforeValues[$i]) -> $($afterValues[$i])"`,
		// The derived total is checked against the evidence it is built from.
		`if ($afterValues[$failureClusterInvariant] -ge $afterValues[$failInvariant]) { break }`,
		// The rebuild is asynchronous; the gate waits for the safe condition
		// rather than reading a pass in flight as a regression.
		`Start-Sleep -Seconds 15`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("deploy.ps1 no longer contains:\n%s", required)
		}
	}

	// The old reading: every position monotonic, and a message that named
	// none of them. Both are what made the rollback undiagnosable.
	for _, forbidden := range []string{
		`for ($i = 0; $i -lt $beforeValues.Count; $i++) {`,
		`a production PASS/FAIL/sample/failure-cluster invariant decreased`,
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("deploy.ps1 still contains the undiagnosable monotonic gate:\n%s", forbidden)
		}
	}

	// A tolerated rebuild correction is still recorded. Silence here would
	// make a real collapse look like an ordinary deploy in the log.
	if !strings.Contains(script, `Write-Output ("rebuilt $($invariantNames[$failureClusterInvariant]): "`) {
		t.Error("deploy.ps1 no longer reports a rebuild that lowered the cluster ledger")
	}
}

// Both sides of the coverage check must come from one reading of the database.
// Comparing a cluster total taken after the rollout against a FAIL total taken
// before it would compare two different moments and drift on ingest alone.
func TestClusterCoverageComparesOneReading(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	coverage := `$afterValues[$failureClusterInvariant] -ge $afterValues[$failInvariant]`
	if !strings.Contains(script, coverage) {
		t.Fatalf("deploy.ps1 does not compare the cluster ledger and FAIL from the same reading: want %s", coverage)
	}
	if strings.Contains(script, `$afterValues[$failureClusterInvariant] -ge $beforeValues[$failInvariant]`) {
		t.Error("the coverage check reads FAIL from the pre-deploy snapshot")
	}
}
