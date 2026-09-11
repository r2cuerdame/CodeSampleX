package lightsail

import (
	"os/exec"
	"testing"
)

func TestBoundedAuthoringFunnelDiagnostic(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-I", "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for the bounded authoring funnel diagnostic")
	}
	output, err := exec.Command(python, "-I", "-B", "authoring_funnel_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("authoring funnel regression: %v\n%s", err, output)
	}
	t.Log(string(output))
}
