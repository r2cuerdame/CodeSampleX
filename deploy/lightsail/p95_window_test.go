package lightsail

import (
	"os/exec"
	"testing"
)

func TestP95WindowIsReadOnlyAndAligned(t *testing.T) {
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil || exec.Command(path, "-I", "-c", "import sys; assert sys.version_info >= (3, 8)").Run() != nil {
			continue
		}
		output, err := exec.Command(path, "-I", "-B", "p95_window_test.py", "-v").CombinedOutput()
		if err != nil {
			t.Fatalf("p95 window safety test: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	t.Fatal("Python 3.8+ required for p95 window tests")
}
