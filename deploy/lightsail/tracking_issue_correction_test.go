package lightsail

import (
	"os/exec"
	"testing"
)

func TestObservationTrackingIssueCorrection(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ is required for observation tracking correction tests")
	}
	out, err := exec.Command(python, "-B", "tracking_issue_correction_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("tracking correction regressions failed: %v\n%s", err, out)
	}
	t.Log(string(out))
}
