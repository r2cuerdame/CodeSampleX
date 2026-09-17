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

// envKeysReadBy extracts the CSX_* variable names one function of
// internal/serverstore/config.go reads, from the source it is parsed in
// rather than from a second list here: a hand-copied list is the thing that
// goes stale.
//
// The name pattern is deliberately every CSX_* variable and not only
// CSX_DB_*. The original guard matched `CSX_DB_[A-Z_]+`, which is the shape
// of the pool policy and nothing else, so a rollback lever the runbook
// promises but that is not spelled CSX_DB_ -- CSX_GOVERNOR_ENABLED, #454's
// one-variable way back -- fell through the exact test whose job is catching
// exactly that. atLeast is the floor below which the extractor is assumed to
// have stopped matching the source at all.
func envKeysReadBy(t *testing.T, fn string, atLeast int) []string {
	t.Helper()
	config := readDeployFixture(t, filepath.Join("..", "..", "internal", "serverstore", "config.go"))
	start := strings.Index(config, "func "+fn)
	if start < 0 {
		t.Fatalf("internal/serverstore/config.go no longer declares %s; this guard is reading the wrong source", fn)
	}
	body := config[start:]
	if end := strings.Index(body, "\nfunc "); end > 0 {
		body = body[:end]
	}
	found := regexp.MustCompile(`"(CSX_[A-Z0-9_]+)"`).FindAllStringSubmatch(body, -1)
	seen := map[string]bool{}
	var keys []string
	for _, m := range found {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	if len(keys) < atLeast {
		t.Fatalf("only %d CSX_* keys found in %s (%v); the extractor stopped matching the source", len(keys), fn, keys)
	}
	return keys
}

// poolPolicyEnvKeys are the variable names PoolPolicyFromEnv reads.
func poolPolicyEnvKeys(t *testing.T) []string {
	t.Helper()
	return envKeysReadBy(t, "PoolPolicyFromEnv", 8)
}

// rollbackLeverEnvKeys are every no-build lever docs/operations.md promises
// an operator during an incident: the pool policy, plus the ones read in
// ConfigFromEnv that are named here because they are documented as
// rollbacks rather than as ordinary settings.
//
// ConfigFromEnv reads dozens of variables and most are not rollbacks, so
// this half is an explicit list -- but it is an explicit list that is itself
// checked against the source (each name must actually appear in
// ConfigFromEnv), so a lever that gets renamed or dropped fails here instead
// of quietly passing.
func rollbackLeverEnvKeys(t *testing.T) []string {
	t.Helper()
	documentedLevers := []string{"CSX_GOVERNOR_ENABLED", "CSX_BUILDER_MODE"}
	read := map[string]bool{}
	for _, key := range envKeysReadBy(t, "ConfigFromEnv", 10) {
		read[key] = true
	}
	keys := poolPolicyEnvKeys(t)
	for _, lever := range documentedLevers {
		if !read[lever] {
			t.Fatalf("%s is listed here as a rollback lever but ConfigFromEnv does not read it; "+
				"either the server stopped honouring it or it was renamed", lever)
		}
		keys = append(keys, lever)
	}
	return keys
}

func TestPoolGuardRollbackReachesTheServerProcess(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")

	for _, key := range rollbackLeverEnvKeys(t) {
		if !strings.Contains(server, key+": ${"+key+":-}") {
			t.Errorf("compose server service does not forward %s; writing it into .env would change nothing, and docs/operations.md promises it is the rollback", key)
		}
		// The forwarding must be inert when nobody sets anything: an empty
		// value is ignored by PoolPolicyFromEnv and by ConfigFromEnv's lever
		// parses, so unset stays the shipped policy. A literal default here
		// would ship a policy nobody chose. (This is asserted per lever, not
		// over the whole service: other CSX_* settings in the same block --
		// CSX_PUBLIC_CHECK, CSX_SNAPSHOT_INTERVAL -- do carry compose-level
		// defaults on purpose, and they are not rollback levers.)
		defaulted := regexp.MustCompile(regexp.QuoteMeta(key) + `: \$\{` + regexp.QuoteMeta(key) + `:-(.+)\}`).FindStringSubmatch(server)
		if defaulted != nil {
			t.Errorf("%s is forwarded with default %q; unset must mean the shipped policy, not a value chosen in the compose file", key, defaulted[1])
		}
	}
}

// The runbook is the other half: whatever the compose forwards, an operator
// follows docs/operations.md, so the two have to describe the same act.
func TestOperationsRunbookMatchesTheWiredRollback(t *testing.T) {
	doc := readDeployFixture(t, filepath.Join("..", "..", "docs", "operations.md"))
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")

	for _, key := range rollbackLeverEnvKeys(t) {
		if !strings.Contains(doc, key) {
			t.Errorf("docs/operations.md never names %s, which the server reads and the compose forwards", key)
		}
	}
	if !strings.Contains(doc, "CSX_DB_POOL_GUARD=off") {
		t.Fatal("docs/operations.md no longer states the one-variable rollback")
	}
	if !strings.Contains(doc, "CSX_GOVERNOR_ENABLED=off") {
		t.Error("docs/operations.md no longer states the resource governor's one-variable rollback")
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

// The runbook tells an operator which lines to grep for while an incident is
// running. Those lines are emitted from two other packages, so nothing but a
// test keeps the doc's spelling and the source's spelling together.
//
// This is not hypothetical: the runbook shipped "csx-builder: paused by the
// resource governor..." for a prefix no binary sets -- nothing under cmd/
// calls log.SetPrefix, so a line logged by internal/compatibility says
// "compatibility:" whichever process ran the Builder -- and an operator
// grepping for it mid-incident would have found nothing at all and concluded
// the governor was not involved.
func TestOperationsRunbookQuotesTheGovernorLogLines(t *testing.T) {
	doc := readDeployFixture(t, filepath.Join("..", "..", "docs", "operations.md"))

	leader := readDeployFixture(t, filepath.Join("..", "..", "internal", "compatibility", "leader.go"))
	builderLines := regexp.MustCompile(`logf\("(compatibility: builder (?:paused by|resumed)[^"]*)"\)`).
		FindAllStringSubmatch(leader, -1)
	if len(builderLines) != 2 {
		t.Fatalf("expected the pause and resume log lines in internal/compatibility/leader.go, found %d: %v",
			len(builderLines), builderLines)
	}
	for _, m := range builderLines {
		if !strings.Contains(doc, m[1]) {
			t.Errorf("docs/operations.md does not quote the line the Builder actually emits: %q", m[1])
		}
	}
	// And the prefix that was wrong must not come back.
	for _, wrong := range []string{
		"csx-builder: paused by the resource governor",
		"csx-builder: resumed; the governor cleared",
	} {
		if strings.Contains(doc, wrong) {
			t.Errorf("docs/operations.md still tells an operator to grep for %q; no binary sets that prefix, "+
				"the line comes from package compatibility", wrong)
		}
	}

	// The server half of the same runbook block.
	governor := readDeployFixture(t, filepath.Join("..", "..", "cmd", "csx-server", "governor.go"))
	for _, prefix := range []string{
		"csx-server: governor paused background work reason=",
		"csx-server: governor resumed background work after=",
	} {
		if !strings.Contains(governor, prefix) {
			t.Errorf("cmd/csx-server/governor.go no longer logs %q", prefix)
		}
		if !strings.Contains(doc, prefix) {
			t.Errorf("docs/operations.md does not quote the governor's own line %q", prefix)
		}
	}
}
