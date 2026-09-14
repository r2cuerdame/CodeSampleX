package lightsail

import (
	"os/exec"
	"testing"
)

func TestPreactivationDeployLockRecovery(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for deploy lock recovery tests")
	}
	output, err := exec.Command(python, "-B", "recover_deploy_lock_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("deploy lock recovery regression failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
