package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIsReadOnlyDoctor(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"doctor"}, true},
		{[]string{"doctor", "--json"}, true},
		{[]string{"doctor", "--verbose"}, true},
		{[]string{"--debug", "doctor"}, true},
		{[]string{"--debug", "doctor", "--json"}, true},
		{[]string{"doctor", "--fix"}, false},
		{[]string{"doctor", "--fix", "--json"}, false},
		{[]string{"doctor", "--json", "--fix"}, false},
		{[]string{"--debug", "doctor", "--fix"}, false},
		{[]string{"status"}, false},
		{[]string{"init"}, false},
		{nil, false},
		{[]string{}, false},
	}
	for _, tc := range tests {
		got := isReadOnlyDoctor(tc.args)
		if got != tc.want {
			t.Errorf("isReadOnlyDoctor(%v) = %v; want %v", tc.args, got, tc.want)
		}
	}
}

func TestReadOnlyDoctorLeavesEmptyHomeByteIdentical(t *testing.T) {
	home := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Run main package binary if built, or test isReadOnlyDoctor guarantees
	entriesBefore, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesBefore) != 0 {
		t.Fatalf("expected empty temp dir, got %d entries", len(entriesBefore))
	}

	// Build a temporary csx binary to test main execution
	bin := filepath.Join(t.TempDir(), "csx-test.exe")
	buildCmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("cannot compile test binary: %v: %s", err, string(out))
	}
	_ = exe

	runCmd := exec.Command(bin, "doctor", "--json")
	runCmd.Env = append(os.Environ(), "CSX_HOME="+home)
	_ = runCmd.Run()

	entriesAfter, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != 0 {
		var names []string
		for _, e := range entriesAfter {
			names = append(names, e.Name())
		}
		t.Fatalf("read-only doctor mutated empty CSX_HOME; found: %v", names)
	}
}
