package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrationBudgetCalculatesActualWorkflowTimeouts(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	step := productionWorkflowStep(t, productionWorkflow(t), "Calculate separate migration and deployment budgets")
	start := strings.Index(step, "        run: |\n")
	if start < 0 {
		t.Fatal("budget calculator not found")
	}
	lines := strings.Split(step[start+len("        run: |\n"):], "\n")
	var program strings.Builder
	for _, line := range lines {
		if strings.HasPrefix(line, "          ") {
			program.WriteString(strings.TrimPrefix(line, "          "))
			program.WriteByte('\n')
		} else if strings.TrimSpace(line) != "" {
			break
		}
	}
	for _, tc := range []struct{ input, want string }{
		{"60", "step_minutes=25\njob_minutes=28\n"},
		{"61", "step_minutes=26\njob_minutes=29\n"},
		{"1200", "step_minutes=44\njob_minutes=47\n"},
		{"1800", "step_minutes=54\njob_minutes=57\n"},
		{"0", ""}, {"59", ""}, {"1801", ""}, {"99999999999999999999", ""},
		{"1200;exit 0", ""}, {"", ""}, {"-1", ""}, {"60.5", ""},
	} {
		t.Run(tc.input, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "outputs")
			cmd := exec.Command(bash, "-c", program.String())
			cmd.Env = append(os.Environ(), "MIGRATION_TIMEOUT="+tc.input, "GITHUB_OUTPUT="+filepath.ToSlash(output))
			log, err := cmd.CombinedOutput()
			if (err == nil) != (tc.want != "") {
				t.Fatalf("budget acceptance: %v: %s", err, log)
			}
			got, _ := os.ReadFile(output)
			if string(got) != tc.want {
				t.Fatalf("outputs %q, want %q", got, tc.want)
			}
		})
	}
}
