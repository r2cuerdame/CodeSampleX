package lightsail

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestReconciliationProvenance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.CommandContext(ctx, path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for reconciliation provenance contracts")
	}
	output, err := exec.CommandContext(ctx, python, "-B", "reconciliation_provenance_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("reconciliation provenance tests: %v\n%s", err, output)
	}
	t.Log(string(output))
}
