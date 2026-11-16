package main

import (
	"os/exec"
	"runtime"
	"testing"
)

func TestKPIWorldStateCheck(t *testing.T) {
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "python3"}
	}
	var python string
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3 is required for the KPI world state tests")
	}
	if out, err := exec.Command(python, "-B", "kpi_world_state_test.py").CombinedOutput(); err != nil {
		t.Fatalf("KPI world state suite: %v\n%s", err, out)
	}
}
