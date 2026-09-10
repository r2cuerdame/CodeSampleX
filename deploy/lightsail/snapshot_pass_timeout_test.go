package lightsail

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// CSX_SNAPSHOT_PASS_TIMEOUT is the ceiling that bounds a builder pass.
// Like the pool ceilings, its rollback (CSX_SNAPSHOT_PASS_TIMEOUT=0) is only as
// good as its delivery: compose reads .env only for ${...} interpolation, so
// without being forwarded in the server environment, setting it in .env does
// not reach the process.
func TestSnapshotPassTimeoutForwardedByCompose(t *testing.T) {
	compose := readDeployFixture(t, filepath.Join("..", "docker-compose.yml"))
	server := composeService(t, compose, "server")

	const key = "CSX_SNAPSHOT_PASS_TIMEOUT"
	if !strings.Contains(server, key+": ${"+key+":-}") {
		t.Fatalf("compose server service does not forward %s; writing it into .env would change nothing", key)
	}

	// Unset must be inert: an empty value keeps the compiled default.
	// A hardcoded default here would override the server's compiled constant.
	defaulted := regexp.MustCompile(`CSX_SNAPSHOT_PASS_TIMEOUT: \$\{CSX_SNAPSHOT_PASS_TIMEOUT:-(.+)\}`).FindStringSubmatch(server)
	if defaulted != nil {
		t.Fatalf("%s is forwarded with literal default %q; unset must be inert", key, defaulted[1])
	}
}

func TestOperationsRunbookDocumentsSnapshotPassTimeoutRollback(t *testing.T) {
	doc := readDeployFixture(t, filepath.Join("..", "..", "docs", "operations.md"))
	if !strings.Contains(doc, "CSX_SNAPSHOT_PASS_TIMEOUT") {
		t.Fatal("docs/operations.md does not document CSX_SNAPSHOT_PASS_TIMEOUT")
	}
	if !strings.Contains(doc, "CSX_SNAPSHOT_PASS_TIMEOUT=0") {
		t.Fatal("docs/operations.md does not explain the 0 rollback")
	}

	// The runbook section must tell the operator to recreate the server container
	// so the .env change takes effect.
	sectionIdx := strings.Index(doc, "### When the builder stops")
	if sectionIdx < 0 {
		t.Fatal("docs/operations.md missing '### When the builder stops' section")
	}
	section := doc[sectionIdx:]
	if end := strings.Index(section, "\n## "); end > 0 {
		section = section[:end]
	}
	if !strings.Contains(section, "docker compose up -d server") {
		t.Error("When the builder stops section does not instruct operator to run `docker compose up -d server`")
	}
}
