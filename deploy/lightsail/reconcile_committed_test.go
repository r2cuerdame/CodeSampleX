package lightsail

import (
	"os/exec"
	"testing"
)

// Reconciliation must stay read-only and retain the original owner in every
// failure path. Run the deterministic host fixtures on Linux and Windows CI.
func TestCommittedDeploymentReconciliation(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ is required to validate committed deployment reconciliation")
	}
	output, err := exec.Command(python, "-B", "reconcile_committed_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("committed deployment reconciliation tests failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
