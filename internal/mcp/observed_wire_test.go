package mcp

import (
	"context"
	"github.com/r2cuerdame/codesamplex/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObservedChildOutputNeverWritesOnMCPProcessStdout(t *testing.T) {
	outputFile := filepath.Join(t.TempDir(), "protocol-stdout")
	wire, err := os.Create(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = wire
	defer func() { os.Stdout = original; wire.Close() }()
	t.Setenv("CSX_TEST_STDOUT_FAILURE", "1")
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	code, _, _, _, output, err := runObserved(context.Background(), nil, nil, cfg, nil, nil, []string{os.Args[0], "-test.run=^TestHelperFailureOnStdout$"}, t.TempDir())
	os.Stdout = original
	wire.Close()
	if err != nil || code != 1 || !strings.Contains(output.Stdout, "TS2352") {
		t.Fatalf("child result lost: %d %v %q", code, err, output.Stdout)
	}
	raw, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 {
		t.Fatalf("child output leaked onto the MCP JSON-RPC stdout stream: %q", raw)
	}
}
