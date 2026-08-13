package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// csx search --json against a seeded engine, served through the running
// daemon via the client (plan P4.4 acceptance).
func TestSearchJSONViaDaemonClient(t *testing.T) {
	home := newCLIHome(t, nil)
	d := startCLIDaemon(t, home)
	seedCLISample(t, d, "sha256:cli1")

	out, code := captureStdout(t, func() int {
		return Main([]string{"search", "upload", "multipart", "form", "with", "axios",
			"--json", "--package", "pkg:npm/axios@1.12.0"})
	})
	if code != 0 {
		t.Fatalf("search exit = %d, output:\n%s", code, out)
	}

	var resp domain.SearchResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output is not a SearchResponse: %v\n%s", err, out)
	}
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("miss=%v results=%d, want hit", resp.Miss, len(resp.Results))
	}
	if resp.Results[0].SampleID != "sha256:cli1" {
		t.Errorf("top sample = %q", resp.Results[0].SampleID)
	}

	// The daemon records search hits; the direct fallback does not.
	// A recorded hit proves the query went through the daemon client.
	hits, err := d.DB.ListHits(context.Background(), 10)
	if err != nil {
		t.Fatalf("list hits: %v", err)
	}
	if len(hits) == 0 {
		t.Error("no hit recorded — search did not go through the daemon")
	}
}

// Without a daemon the search falls back to the local engine directly.
func TestSearchTextDirectFallback(t *testing.T) {
	home := newCLIHome(t, nil)
	// Seed through a daemon instance that never Runs (store access only).
	d := startCLIDaemon(t, home)
	seedCLISample(t, d, "sha256:cli2")

	out, code := captureStdout(t, func() int {
		return Main([]string{"search", "upload", "multipart", "form", "with", "axios"})
	})
	if code != 0 {
		t.Fatalf("search exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "MATCH:") {
		t.Errorf("text output missing MATCH line:\n%s", out)
	}
	if !strings.Contains(out, "Evidence") {
		t.Errorf("text output missing Evidence section (§11.5):\n%s", out)
	}
}

func TestSearchUsageError(t *testing.T) {
	newCLIHome(t, nil)
	if code := Main([]string{"search"}); code != 2 {
		t.Fatalf("bare search exit = %d, want 2", code)
	}
}
