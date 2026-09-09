package lightsail

import (
	"os/exec"
	"testing"
)

// Run the behavioral host-recovery and real Git/PowerShell source-identity
// tests in canonical CI. These tests never connect to Docker or production.
func TestOfflineMigrationRecovery(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil {
			if err = exec.Command(path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run(); err == nil {
				python = path
				break
			}
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ is required to validate the production recovery supervisor")
	}
	output, err := exec.Command(python, "-B", "offline_migration_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("offline migration recovery tests failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
