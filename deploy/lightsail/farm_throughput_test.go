package lightsail

import (
	"os/exec"
	"testing"
)

func TestBoundedFarmScalarThroughput(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-I", "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for the scalar throughput diagnostic")
	}
	output, err := exec.Command(python, "-I", "-B", "farm_throughput_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("scalar throughput regression: %v\n%s", err, output)
	}
	t.Log(string(output))
}
