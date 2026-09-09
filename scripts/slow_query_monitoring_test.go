package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPostgreSQLSlowQueryMonitoringConfig(t *testing.T) {
	composePath := filepath.Join("..", "deploy", "docker-compose.yml")
	content, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("failed to read compose file: %v", err)
	}
	text := string(content)

	requiredFlags := []string{
		"-c shared_preload_libraries=pg_stat_statements",
		"-c pg_stat_statements.track=top",
		"-c pg_stat_statements.max=5000",
		"-c track_io_timing=on",
		"-c log_min_duration_statement=2000",
		"-c log_parameter_max_length=0",
		"-c log_parameter_max_length_on_error=0",
	}

	for _, flag := range requiredFlags {
		if !strings.Contains(text, flag) {
			t.Errorf("docker-compose.yml db service is missing required postgres flag: %q", flag)
		}
	}
}

func TestOperationsDocumentsSlowQueryMonitoring(t *testing.T) {
	docPath := filepath.Join("..", "docs", "operations.md")
	content, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("failed to read operations doc: %v", err)
	}
	text := string(content)

	requiredTerms := []string{
		"pg_stat_statements",
		"track_io_timing = on",
		"log_min_duration_statement = 2000",
		"log_parameter_max_length = 0",
		"pg-slow-queries.py",
		"collect-pg-slow-queries.sh",
		"pg_stat_statements_reset()",
	}

	for _, term := range requiredTerms {
		if !strings.Contains(text, term) {
			t.Errorf("docs/operations.md is missing required slow query documentation term: %q", term)
		}
	}
}

func TestSlowQueryDiagnosticScriptsExist(t *testing.T) {
	for _, rel := range []string{"collect-pg-slow-queries.sh", "pg-slow-queries.py"} {
		info, err := os.Stat(rel)
		if err != nil {
			t.Fatalf("script %s missing: %v", rel, err)
		}
		if info.Size() == 0 {
			t.Errorf("script %s is empty", rel)
		}
	}
}
