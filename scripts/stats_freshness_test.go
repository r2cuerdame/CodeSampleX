package main

import (
	"os/exec"
	"runtime"
	"testing"
)

// #517: the stats-clock freshness check judges fixture stamps, no network.
func TestStatsFreshnessCheck(t *testing.T) {
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
		t.Fatal("Python 3 is required for the stats freshness check tests")
	}
	if out, err := exec.Command(python, "stats_freshness_test.py").CombinedOutput(); err != nil {
		t.Fatalf("stats freshness suite: %v\n%s", err, out)
	}
}
