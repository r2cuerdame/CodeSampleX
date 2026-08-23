package lightsail

// The R2C-58 pool guard is only as safe as its way back. docs/operations.md
// calls the rollback "one variable" -- write CSX_DB_POOL_GUARD=off into the
// compose .env and bring the stack up -- and R2C-110 measured on production
// that this did nothing at all: Compose reads .env for `${...}` interpolation
// only, so a variable no service names never reaches the process. The panel
// still reported the guard as on, the container was not even recreated, and
// an operator following the runbook during an incident would have believed
// they had rolled back while the ceilings were still enforced.
//
// A rollback that silently does nothing is worse than no rollback, because it
// is reached for exactly when there is no time to check. These tests pin the
// wiring to the knobs the server actually reads, so a future setting cannot
// be added to the policy and left undeliverable.

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// poolPolicyEnvKeys are the variable names PoolPolicyFromEnv reads, taken
// from the source it is parsed in rather than from a second list here: a
// hand-copied list is the thing that goes stale.
func poolPolicyEnvKeys(t *testing.T) []string {
	t.Helper()
	config := readDeployFixture(t, filepath.Join("..", "..", "internal", "serverstore", "config.go"))
	body := config[strings.Index(config, "func PoolPolicyFromEnv"):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	found := regexp.MustCompile(`"(CSX_DB_[A-Z_]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var keys []string
	for _, m := range found {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	if len(keys) < 8 {
		t.Fatalf("only %d CSX_DB_* keys found in PoolPolicyFromEnv (%v); the extractor stopped matching the source", len(keys), keys)
	}
	return keys
}

func TestPoolGuardRollbackReachesTheServerProcess(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")

	for _, key := range poolPolicyEnvKeys(t) {
		if !strings.Contains(server, key+": ${"+key+":-}") {
			t.Errorf("compose server service does not forward %s; writing it into .env would change nothing, and docs/operations.md promises it is the rollback", key)
		}
	}

	// The forwarding must be inert when nobody sets anything: an empty value
	// is ignored by PoolPolicyFromEnv, so unset stays the shipped policy.
	// A literal default here would ship a policy nobody chose.
	defaulted := regexp.MustCompile(`CSX_DB_[A-Z_]+: \$\{CSX_DB_[A-Z_]+:-(.+)\}`).FindStringSubmatch(server)
	if defaulted != nil {
		t.Errorf("CSX_DB_* is forwarded with default %q; unset must mean the shipped policy, not a value chosen in the compose file", defaulted[1])
	}
}

// The runbook is the other half: whatever the compose forwards, an operator
// follows docs/operations.md, so the two have to describe the same act.
func TestOperationsRunbookMatchesTheWiredRollback(t *testing.T) {
	doc := readDeployFixture(t, filepath.Join("..", "..", "docs", "operations.md"))
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")

	for _, key := range poolPolicyEnvKeys(t) {
		if !strings.Contains(doc, key) {
			t.Errorf("docs/operations.md never names %s, which the server reads and the compose forwards", key)
		}
	}
	if !strings.Contains(doc, "CSX_DB_POOL_GUARD=off") {
		t.Fatal("docs/operations.md no longer states the one-variable rollback")
	}
	// Compose only recreates a container whose configuration changed, and an
	// operator who reads "Running" during an incident has no way to tell an
	// unchanged container from a restarted one. The runbook must name the
	// step that actually restarts the server.
	if !strings.Contains(doc, "docker compose up -d server") {
		t.Error("docs/operations.md does not tell the operator how to restart the server after editing .env")
	}
	if !strings.Contains(server, "CSX_DB_POOL_GUARD") {
		t.Error("the runbook's rollback variable is not forwarded by the compose server service")
	}
}
