package lightsail

import (
	"os/exec"
	"testing"
)

func TestCommittedOwnerReconciliation(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for owner reconciliation regressions")
	}
	output, err := exec.Command(python, "-B", "reconcile_host_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("owner reconciliation regression: %v\n%s", err, output)
	}
	t.Log(string(output))
}
