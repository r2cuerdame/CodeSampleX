package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPostgreSQLMonitoringDisablesSlowStatementLogging(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "deploy", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{
		"shared_preload_libraries=pg_stat_statements",
		"log_min_duration_statement=-1",

		"log_parameter_max_length=0", "log_parameter_max_length_on_error=0",
	} {
		if !strings.Contains(string(content), "-c "+flag) {
			t.Errorf("missing privacy setting: %s", flag)
		}
	}
}

func TestPostgreSQLMonitoringCLI(t *testing.T) {
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
		t.Fatal("Python 3 is required for diagnostic CLI regression tests")
	}
	if out, err := exec.Command(python, "pg_slow_queries_test.py").CombinedOutput(); err != nil {
		t.Fatalf("diagnostic CLI regression suite: %v\n%s", err, out)
	}
}
