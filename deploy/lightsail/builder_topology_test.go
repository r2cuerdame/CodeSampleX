package lightsail

// CSX-451's own version of poolguard_rollback_test.go's guarantee: the
// standalone Builder's topology switch and its pool ceilings are only as
// safe as their way back to the in-process Builder, and that rollback is
// only real if compose actually forwards the variables the process reads.
//
// See docs/operations.md "Builder runtime topology".

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// builderPoolEnvKeys are the variable names BuilderPoolPolicyFromEnv reads,
// taken from the source rather than hand-copied here -- see
// poolPolicyEnvKeys in poolguard_rollback_test.go for why.
func builderPoolEnvKeys(t *testing.T) []string {
	t.Helper()
	config := readDeployFixture(t, filepath.Join("..", "..", "internal", "serverstore", "builderconfig.go"))
	body := config[strings.Index(config, "func BuilderPoolPolicyFromEnv"):]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	found := regexp.MustCompile(`"(CSX_BUILDER_DB_[A-Z_]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var keys []string
	for _, m := range found {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	if len(keys) < 5 {
		t.Fatalf("only %d CSX_BUILDER_DB_* keys found in BuilderPoolPolicyFromEnv (%v); the extractor stopped matching the source", len(keys), keys)
	}
	return keys
}

func TestBuilderModeAndPoolCeilingsReachTheirProcesses(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")
	builder := composeService(t, compose, "builder")

	if !strings.Contains(server, "CSX_BUILDER_MODE: ${CSX_BUILDER_MODE:-}") {
		t.Error("compose server service does not forward CSX_BUILDER_MODE; the standalone/in-process rollback would silently do nothing")
	}
	if strings.Contains(server, "CSX_BUILDER_MODE: ${CSX_BUILDER_MODE:-standalone}") {
		t.Error("CSX_BUILDER_MODE is forwarded with a literal default of standalone; unset must mean the shipped (in-process) default, not a value chosen in the compose file")
	}

	for _, key := range builderPoolEnvKeys(t) {
		if !strings.Contains(builder, key+": ${"+key+":-}") {
			t.Errorf("compose builder service does not forward %s; writing it into .env would change nothing", key)
		}
		if strings.Contains(server, key) {
			t.Errorf("%s is forwarded to the server service too; the whole point of CSX_BUILDER_DB_* is that it never touches csx-server's own pool", key)
		}
	}

	// The physical-separation guarantee, checked the same way
	// TestPoolGuardRollbackReachesTheServerProcess checks the ceiling
	// guarantee: the two processes' pool knobs must never collide.
	for _, key := range poolPolicyEnvKeys(t) {
		if strings.Contains(builder, key+":") && !strings.HasPrefix(key, "CSX_BUILDER") {
			t.Errorf("csx-server's %s is forwarded to the builder service; the standalone Builder must size its pool from CSX_BUILDER_DB_*, never csx-server's CSX_DB_*", key)
		}
	}
}

// The Builder never runs migrations (docs/operations.md), so it must not be
// allowed to start before csx-server's own migrate-then-serve boot has
// proven itself healthy.
func TestBuilderServiceWaitsForServerToBeHealthyNotJustRunning(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	builder := composeService(t, compose, "builder")
	if !strings.Contains(builder, "depends_on:") || !strings.Contains(builder, "server:") || !strings.Contains(builder, "condition: service_healthy") {
		t.Fatal("builder service does not depend on server being healthy; it could start and query a database csx-server has not migrated yet")
	}
}

// Same boundary poolguard_rollback_test.go enforces for `server`: the
// Builder's own health/ready/progress endpoints are ops-only and must never
// be reachable from outside the compose network.
func TestBuilderStatusEndpointsAreNeverPublishedOutsideTheComposeNetwork(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	builder := composeService(t, compose, "builder")
	if !strings.Contains(builder, "expose:") {
		t.Fatal("builder service does not declare expose; its listener boundary is undocumented")
	}
	if strings.Contains(builder, "ports:") {
		t.Fatal("builder service publishes ports; /progress and /healthz would be reachable from outside the host")
	}
}

// The runbook and the compose file must describe the same act, exactly like
// TestOperationsRunbookMatchesTheWiredRollback already requires for
// CSX_DB_*.
func TestOperationsRunbookMatchesTheBuilderTopology(t *testing.T) {
	doc := readDeployFixture(t, filepath.Join("..", "..", "docs", "operations.md"))
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	builder := composeService(t, compose, "builder")

	if !strings.Contains(doc, "CSX_BUILDER_MODE") {
		t.Error("docs/operations.md never names CSX_BUILDER_MODE, which both processes' boot path reads")
	}
	for _, key := range builderPoolEnvKeys(t) {
		if !strings.Contains(doc, key) {
			t.Errorf("docs/operations.md never names %s, which the builder service forwards", key)
		}
	}
	if !strings.Contains(builder, "CSX_BUILDER_LEASE_TTL") {
		t.Error("builder service does not forward its own lease timings")
	}
}
