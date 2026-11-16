package main

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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
	entriesBefore, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesBefore) != 0 {
		t.Fatalf("expected empty temp dir, got %d entries", len(entriesBefore))
	}

	bin := filepath.Join(t.TempDir(), "csx-test.exe")
	buildCmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Skipf("cannot compile test binary: %v: %s", err, string(out))
	}

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

func TestMCPStdioRoundTripOnPayloadBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds payload binary")
	}
	home := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	bin := filepath.Join(t.TempDir(), "csx-payload"+ext)
	buildCmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("compile payload binary: %v: %s", err, string(out))
	}

	cmd := exec.Command(bin, "mcp")
	cmd.Env = append(os.Environ(), "CSX_HOME="+home, "DO_NOT_TRACK=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdout)
	_, err = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-06-18\",\"capabilities\":{},\"clientInfo\":{\"name\":\"test\",\"version\":\"1\"}}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	initLine, err := reader.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("read initialize response: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(initLine, `"serverInfo"`) {
		t.Fatalf("unexpected initialize response: %s", initLine)
	}
	_, _ = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"))
	_, _ = stdin.Write([]byte("{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/list\",\"params\":{}}\n"))
	toolsLine, err := reader.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("read tools/list response: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(toolsLine, `"tools"`) {
		t.Fatalf("unexpected tools/list response: %s", toolsLine)
	}
	_ = stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("mcp exit error: %v (stderr=%s)", err, stderr.String())
	}
}
