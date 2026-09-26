package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPerfSLODecision runs the fixture tests for the #511 violation/recovery
// decision. No network and no gh: gh is replaced by a recorder.
func TestPerfSLODecision(t *testing.T) {
	var python string
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err == nil && exec.Command(path, "-I", "-c", "import sys; assert sys.version_info >= (3, 8)").Run() == nil {
			python = path
			break
		}
	}
	if python == "" {
		t.Fatal("Python 3.8+ required for the performance SLO probe")
	}
	output, err := exec.Command(python, "-I", "-B", "perf_slo_test.py", "-v").CombinedOutput()
	if err != nil {
		t.Fatalf("performance SLO decision regression: %v\n%s", err, output)
	}
	t.Log(string(output))
}

// TestPerfSLOBaselineIsRecorded keeps the stored baseline honest: every path
// has a target, and the baseline names the date and the commit it ran on.
func TestPerfSLOBaselineIsRecorded(t *testing.T) {
	raw, err := os.ReadFile("perf-slo-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Rounds   int `json:"rounds"`
		Baseline *struct {
			MeasuredAt string `json:"measuredAt"`
			Revision   string `json:"revision"`
			Version    string `json:"version"`
		} `json:"baseline"`
		Paths []struct {
			Name   string   `json:"name"`
			Path   string   `json:"path"`
			Target *float64 `json:"targetSeconds"`
			Source string   `json:"targetSource"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Rounds != 20 {
		t.Errorf("rounds = %d, want 20", cfg.Rounds)
	}
	if cfg.Baseline == nil || cfg.Baseline.MeasuredAt == "" || len(cfg.Baseline.Revision) != 40 || cfg.Baseline.Version == "" {
		t.Fatalf("baseline must record measuredAt, version and a 40-hex revision: %+v", cfg.Baseline)
	}
	want := map[string]bool{"/healthz": false, "/version": false, "/v1/stats": false, "/v1/shards/": false, "/v1/verification/jobs": false}
	for _, p := range cfg.Paths {
		if p.Target == nil || *p.Target <= 0 {
			t.Errorf("%s has no target", p.Name)
		}
		if p.Source != "baseline-p95" && p.Source != "known-good-p95" {
			t.Errorf("%s targetSource = %q", p.Name, p.Source)
		}
		for prefix := range want {
			if p.Path == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(p.Path, prefix)) ||
				strings.HasPrefix(p.Path, prefix+"?") {
				want[prefix] = true
			}
		}
	}
	for prefix, seen := range want {
		if !seen {
			t.Errorf("no SLO path for %s", prefix)
		}
	}
}

// TestPerfSLOWorkflowStaysLight pins the cadence and the lightness contract.
func TestPerfSLOWorkflowStaysLight(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "perf-slo.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := string(raw)
	for _, s := range []string{
		"workflows: [Production deploy]",
		"schedule:",
		"cron:",
		"scripts/perf-slo.py measure",
		"scripts/perf-slo.py reconcile",
	} {
		if !strings.Contains(wf, s) {
			t.Errorf("workflow is missing %q", s)
		}
	}
	for _, s := range []string{
		"group: codesamplex-production", // a queued probe must never displace a deploy
		"environment: codesamplex-production",
		"ssh ",
		"secrets.",
		"/v2/search",
		"/v1/authoring",
	} {
		if strings.Contains(wf, s) {
			t.Errorf("workflow must not contain %q", s)
		}
	}
	cfg, err := os.ReadFile("perf-slo-baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"/search", "/authoring", "/claim", "/presence"} {
		if strings.Contains(string(cfg), s) {
			t.Errorf("probed paths must stay read-only and slot-free; found %q", s)
		}
	}
}
