package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/evidence"
)

// The scanner has detected bun.lock / deno.json / an electron dependency
// since the beginning, and nothing in the codebase ever read the result.
// The doc comment says it "is used only to widen search environments" — a
// Bun project was searched as plain node, and execution-context grading,
// which exists precisely to separate those, never saw a difference.
func TestBunProjectIsDetectedAsItsOwnExecutionContext(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"app","dependencies":{"axios":"^1.12.0"}}`)
	write("bun.lock", "{}")

	res, err := evidence.Scan(context.Background(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.TargetContext != "bun" {
		t.Fatalf("targetContext = %q, want bun", res.TargetContext)
	}
	// The scan's own environment describes the machine, not the target —
	// which is why the hint has to be applied on top of it.
	if res.Env.ExecutionContext == "bun" {
		t.Skip("the scan environment already reports bun; the hint is redundant here")
	}
}
