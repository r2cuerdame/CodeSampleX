package lightsail

import (
	"path/filepath"
	"strconv"
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

// failure_clusters is a rebuildable materialization. A full builder pass can
// regroup the same source FAIL evidence and legitimately reduce its summed
// observation count (production did exactly that: 20098 -> 20096). The deploy
// must still reject source loss, an empty materialization, or a cluster whose
// quality breakdown no longer adds up to its observation count.
func TestDeploySeparatesSourceMonotonicityFromDerivedClusterConsistency(t *testing.T) {
	deploy := readDeployFixture(t, "deploy.ps1")
	collector := readDeployFixture(t, "collect-production-evidence.sh")

	for _, required := range []string{
		`$sourceInvariantIndexes = @(0, 1, 2, 4, 5)`,
		`foreach ($i in $sourceInvariantIndexes)`,
		`$afterValues[1] -gt 0 -and $afterValues[3] -le 0`,
		`$afterValues[6] -ne 0`,
		`$builderFresh -eq 1`,
		`the new server did not complete a fresh full builder pass`,
		`failure-cluster observation delta: $failureClusterObservationDelta`,
		`failure-cluster ledger is internally inconsistent`,
	} {
		if !strings.Contains(deploy, required) {
			t.Errorf("deploy does not enforce the split source/derived invariant: missing %q", required)
		}
	}
	if strings.Contains(deploy, `for ($i = 0; $i -lt $beforeValues.Count; $i++)`) {
		t.Error("deploy still applies monotonicity to the rebuildable failure-cluster total")
	}
	if strings.Contains(deploy, `PASS/FAIL/sample/failure-cluster invariant decreased`) {
		t.Error("deploy still reports the derived failure-cluster total as a monotonic source invariant")
	}
	if strings.Contains(deploy, `$beforeValues[6] -ne 0`) {
		t.Error("deploy blocks the fresh full builder from repairing a pre-existing derived-ledger imbalance")
	}
	markerPos := strings.Index(deploy, `builder_fresh=1`)
	valuePos := strings.Index(deploy, `values=$(docker compose exec -T db psql`)
	if markerPos < 0 || valuePos <= markerPos {
		t.Error("deploy samples derived values before proving the full builder completion marker")
	}

	for name, script := range map[string]string{"deploy": deploy, "collector": collector} {
		for _, required := range []string{
			`jsonb_each(fc.evidence_breakdown)`,
			`item.key NOT IN ('complete','partial','missing','legacy-evidence-incomplete')`,
			`fc.observation_count::numeric <> COALESCE`,
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s does not fail closed on an inconsistent cluster row: missing %q", name, required)
			}
		}
	}
	if !strings.Contains(collector, `'unbalancedFailureClusterRows'`) {
		t.Error("production evidence does not record the derived-ledger consistency result")
	}
	for _, required := range []string{
		`server_started_at=$(docker inspect codesamplex-server-1`,
		`builder_generated_at=$(docker compose exec -T db psql`,
		`builder_fresh=true`,
		`printf 'builder_fresh=%s\n' "$builder_fresh"`,
	} {
		if !strings.Contains(collector, required) {
			t.Errorf("production evidence does not prove a fresh full builder pass: missing %q", required)
		}
	}

	wrapper := readDeployFixture(t, "deploy-production.ps1")
	for _, required := range []string{
		`'server_started_at','builder_generated_at','builder_fresh'`,
		`$after.builder_fresh -ne "true"`,
		`$evidence.failureClusterObservationDelta = [int64]$after.invariants.failureClusterObservations - [int64]$before.invariants.failureClusterObservations`,
	} {
		if !strings.Contains(wrapper, required) {
			t.Errorf("production artifact omits the builder/derived delta proof: missing %q", required)
		}
	}
}

// The fresh-builder gate used to run the complete evidence/materialization
// invariant query on every two-second poll. On the production corpus those
// 90 reads took nineteen minutes and competed with the very full builder pass
// the gate was waiting for. Freshness is one cheap timestamp comparison; the
// full invariant tuple belongs after that marker and is read exactly once.
func TestDeployPollsFreshnessBeforeReadingFullPostDeployInvariants(t *testing.T) {
	deploy := readDeployFixture(t, "deploy.ps1")
	loopStart := strings.Index(deploy, `for ($attempt = 1; $attempt -le 90; $attempt++)`)
	loopEndMarker := `if ($builderFresh -ne 1) { throw "the new server did not complete a fresh full builder pass" }`
	loopEnd := strings.Index(deploy, loopEndMarker)
	if loopStart < 0 || loopEnd <= loopStart {
		t.Fatal("post-deploy fresh-builder polling loop is missing or malformed")
	}
	loop := deploy[loopStart:loopEnd]
	if !strings.Contains(loop, `Invoke-RemoteScript $collectBuilderFreshScript`) {
		t.Error("fresh-builder loop does not use the cheap timestamp-only probe")
	}
	if strings.Contains(loop, `Invoke-RemoteScript $collectInvariantScript`) {
		t.Error("fresh-builder loop still repeats the whole-corpus invariant query")
	}

	fullRead := strings.Index(deploy[loopEnd+len(loopEndMarker):],
		`$invariantsAfter = (Invoke-RemoteScript $collectInvariantScript`)
	if fullRead < 0 {
		t.Error("deploy does not read the full invariant tuple after builder freshness")
	}
	if got := strings.Count(deploy, `Invoke-RemoteScript $collectInvariantScript`); got != 2 {
		t.Errorf("full invariant query invocation count = %d, want pre-deploy + one post-deploy", got)
	}
}

func TestDeployInvariantPolicyAcceptsAReconciledDerivedLedger(t *testing.T) {
	script := readDeployFixture(t, "deploy.ps1")
	const marker = `$sourceInvariantIndexes = @(`
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatal("source invariant index declaration is missing")
	}
	start += len(marker)
	end := strings.Index(script[start:], ")")
	if end < 0 {
		t.Fatal("source invariant index declaration is malformed")
	}
	var sourceIndexes []int
	for _, raw := range strings.Split(script[start:start+end], ",") {
		index, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			t.Fatalf("source invariant index %q is not numeric: %v", raw, err)
		}
		sourceIndexes = append(sourceIndexes, index)
	}

	allows := func(before, after []int64) bool {
		if len(before) != 8 || len(after) != 8 || after[6] != 0 || after[7] != 1 {
			return false
		}
		for _, index := range sourceIndexes {
			if after[index] < before[index] {
				return false
			}
		}
		return after[1] == 0 || after[3] > 0
	}

	liveBefore := []int64{167173, 19262, 108, 20098, 0, 0, 0, 1}
	liveAfter := []int64{167173, 19262, 108, 20096, 0, 0, 0, 1}
	if !allows(liveBefore, liveAfter) {
		t.Error("the exact production reconciliation 20098 -> 20096 is still rejected")
	}
	repairBefore := []int64{167173, 19262, 108, 20098, 0, 0, 1, 1}
	if !allows(repairBefore, liveAfter) {
		t.Error("a fresh full builder cannot repair a pre-existing derived-ledger imbalance")
	}

	for _, tc := range []struct {
		name  string
		after []int64
	}{
		{"raw FAIL loss", []int64{167173, 19261, 108, 20096, 0, 0, 0, 1}},
		{"published sample loss", []int64{167173, 19262, 107, 20096, 0, 0, 0, 1}},
		{"derived ledger disappeared", []int64{167173, 19262, 108, 0, 0, 0, 0, 1}},
		{"derived ledger unbalanced", []int64{167173, 19262, 108, 20096, 0, 0, 1, 1}},
		{"builder not fresh", []int64{167173, 19262, 108, 20096, 0, 0, 0, 0}},
		{"malformed legacy tuple", []int64{167173, 19262, 108, 20096, 0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if allows(liveBefore, tc.after) {
				t.Error("unsafe deployment transition was accepted")
			}
		})
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
