package lightsail

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestReconciliationController(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
		t.Fatal("Python 3.8+ is required for the production reconciliation contract")
	}
	out, err := exec.CommandContext(ctx, python, "-B", "reconcile_production_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("reconciliation controller regression: %v\n%s", err, out)
	}
	t.Log(string(out))
}
