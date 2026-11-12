package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// TestRunRecordsEvidenceEndToEnd is the task acceptance path: in a fixture
// npm project, `csx run -- node -e "process.exit(0)"` records
// PROJECT_PROCESS PASS rows for public deps only and opportunistically
// uploads them. The public verdict is pre-seeded in the local cache so no
// real registry is contacted.
func TestRunRecordsEvidenceEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}

	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// Seed the publicness cache so the checker never leaves the machine.
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	axios := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := db.SetPublicness(context.Background(), axios, "PUBLIC"); err != nil {
		t.Fatalf("seed publicness: %v", err)
	}
	db.Close()

	// Fixture npm project with one public and one private dep.
	proj := t.TempDir()
	files := map[string]string{
		"package.json": `{"name":"fixture-proj","dependencies":{"axios":"^1.12.0","privlib":"file:../privlib"}}`,
		"package-lock.json": `{
  "name": "fixture-proj",
  "lockfileVersion": 3,
  "packages": {
    "": {"dependencies": {"axios": "^1.12.0", "privlib": "file:../privlib"}},
    "node_modules/axios": {"version": "1.12.0", "resolved": "https://registry.npmjs.org/axios/-/axios-1.12.0.tgz"},
    "node_modules/privlib": {"version": "1.0.0", "resolved": "file:../privlib"}
  }
}`,
		"index.js": "const axios = require('axios');\naxios.post('/x', {});\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Chdir(proj)

	if code := Main([]string{"run", "--", "node", "-e", "process.exit(0)"}); code != 0 {
		t.Fatalf("csx run returned %d, want 0", code)
	}

	mu.Lock()
	got := append([]string(nil), bodies...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("server saw %d uploads, want 1", len(got))
	}
	body := got[0]
	if !strings.Contains(body, "pkg:npm/axios@1.12.0") {
		t.Errorf("upload missing public package:\n%s", body)
	}
	if !strings.Contains(body, string(domain.StageProjectProcess)) || !strings.Contains(body, `"result":"PASS"`) {
		t.Errorf("upload missing PROJECT_PROCESS PASS:\n%s", body)
	}
	if strings.Contains(body, "privlib") {
		t.Errorf("private dep leaked into upload:\n%s", body)
	}
	if regexp.MustCompile(`[A-Za-z]:[\\/]|/home/|/Users/`).MatchString(body) {
		t.Errorf("path-like string in upload:\n%s", body)
	}

	// Local DB: rows exist and were marked uploaded by the opportunistic
	// upload; the private dep never entered observations.
	db, err = localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("reopen db: %v", err)
	}
	defer db.Close()
	rows, err := db.PendingObservations(context.Background(), 100)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("rows still pending after successful opportunistic upload: %+v", rows)
	}

	// Exit code passthrough for failures.
	if code := Main([]string{"run", "--", "node", "-e", "process.exit(5)"}); code != 5 {
		t.Fatalf("csx run passthrough returned %d, want 5", code)
	}
}

// Exercise the same Main/run/recorder/upload path as a user, with only the
// Python standard library and a seeded publicness cache (no package downloads).
func TestRunPythonInterpreterBoundary(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("python not installed")
	}
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	var mu sync.Mutex
	var batches []domain.ObservationBatch
	var uploadErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/evidence/batches" {
			var payload struct {
				Batches []domain.ObservationBatch `json:"batches"`
			}
			err := json.NewDecoder(r.Body).Decode(&payload)
			mu.Lock()
			if err != nil {
				uploadErr = err
			} else {
				batches = append(batches, payload.Batches...)
			}
			mu.Unlock()
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	dep := domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"}
	if err := db.SetPublicness(context.Background(), dep, "PUBLIC"); err != nil {
		db.Close()
		t.Fatalf("seed publicness: %v", err)
	}
	db.Close()

	proj := t.TempDir()
	files := map[string]string{
		"requirements.txt": "requests==2.32.3\n",
		"app.py": `import json
import sys
with open("argv.json", "w", encoding="utf-8") as f:
    json.dump(sys.argv[1:], f)
sys.exit(int(sys.argv[-1]))
`,
		"test_pass.py": `import unittest
class SmokeTest(unittest.TestCase):
    def test_pass(self):
        self.assertTrue(True)
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Chdir(proj)

	tests := []struct {
		name   string
		args   []string
		code   int
		stage  domain.Stage
		result domain.Result
	}{
		{"script success", []string{"app.py", "-m", "pytest", "0"}, 0, domain.StageProjectProcess, domain.ResultPass},
		// A silent failure has no diagnostic stage. Its outer stage still
		// identifies the actual process rather than the pytest argument.
		{"script failure", []string{"app.py", "-m", "pytest", "5"}, 5, domain.StageUnknown, domain.ResultFail},
		{"unittest module", []string{"-m", "unittest", "test_pass"}, 0, domain.StageProjectTest, domain.ResultPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mu.Lock()
			batches = nil
			uploadErr = nil
			mu.Unlock()
			argv := append([]string{"run", "--", python}, tt.args...)
			if code := Main(argv); code != tt.code {
				t.Fatalf("csx run returned %d, want %d", code, tt.code)
			}
			if tt.args[0] == "app.py" {
				raw, err := os.ReadFile(filepath.Join(proj, "argv.json"))
				if err != nil {
					t.Fatalf("read program arguments: %v", err)
				}
				var gotArgs []string
				if err := json.Unmarshal(raw, &gotArgs); err != nil {
					t.Fatalf("decode program arguments: %v", err)
				}
				if !slices.Equal(gotArgs, tt.args[1:]) {
					t.Errorf("program arguments = %v, want %v", gotArgs, tt.args[1:])
				}
			}
			mu.Lock()
			got := append([]domain.ObservationBatch(nil), batches...)
			decodeErr := uploadErr
			mu.Unlock()
			if decodeErr != nil {
				t.Fatalf("decode upload: %v", decodeErr)
			}
			if len(got) == 0 {
				t.Fatal("no evidence uploaded")
			}
			var dependencyBatches, commandBatches int
			for _, batch := range got {
				wantStage := tt.stage
				switch {
				case batch.Package == dep.String():
					dependencyBatches++
				case strings.HasPrefix(batch.Package, "pkg:generic/cli/python@"):
					commandBatches++
					// Successful generic CLI evidence describes the process;
					// dependency evidence carries the adapter's check stage.
					if tt.code == 0 {
						wantStage = domain.StageProjectProcess
					}
				default:
					t.Errorf("unexpected package in evidence: %+v", batch)
				}
				if batch.Stage != wantStage || batch.Result != tt.result {
					t.Errorf("unexpected evidence: %+v", batch)
				}
				if tt.args[0] == "app.py" {
					if batch.Stage == domain.StageProjectTest || batch.Stage == domain.StageProjectTypecheck || batch.OuterStage == domain.StageProjectTest || batch.OuterStage == domain.StageProjectTypecheck {
						t.Errorf("program argument misclassified as a check: %+v", batch)
					}
					if tt.code != 0 && (batch.OuterStage != domain.StageProjectProcess || batch.ExitCode == nil || *batch.ExitCode != tt.code || batch.TerminationKind != domain.TerminationExit) {
						t.Errorf("incorrect process failure evidence: %+v", batch)
					}
				}
			}
			if dependencyBatches == 0 || commandBatches == 0 {
				t.Errorf("missing evidence: dependency batches = %d, command batches = %d", dependencyBatches, commandBatches)
			}
		})
	}
}
