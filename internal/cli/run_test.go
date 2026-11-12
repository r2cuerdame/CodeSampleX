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

// TestRunUVPreparationOptions uses installed tools rather than a stand-in. uv
// cannot download Python, resolve dependencies or synchronize a project here;
// the only HTTP traffic needed by csx is to the local evidence receiver.
func TestRunUVPreparationOptions(t *testing.T) {
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Skip("uv not installed; actual uv run acceptance requires uv and Python")
	}
	python, err := exec.LookPath("python")
	if err != nil {
		python, err = exec.LookPath("python3")
	}
	if err != nil {
		t.Skip("Python not installed; actual uv run acceptance requires uv and Python")
	}
	uv, err = filepath.Abs(uv)
	if err != nil {
		t.Fatal(err)
	}
	python, err = filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	t.Setenv("UV_CACHE_DIR", t.TempDir())

	var mu sync.Mutex
	var batches []domain.ObservationBatch
	var uploadErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			http.NotFound(w, r)
			return
		}
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
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	dep := domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"}
	err = db.SetPublicness(context.Background(), dep, "PUBLIC")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

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
from pathlib import Path
class SmokeTest(unittest.TestCase):
    def test_pass(self):
        Path("test-ran").write_text("passed", encoding="utf-8")
        self.assertEqual(2 + 2, 4)
`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(proj)

	for _, tc := range []struct {
		name  string
		args  []string
		stage domain.Stage
		exit  int
	}{
		{"unittest", []string{"-m", "unittest", "test_pass"}, domain.StageProjectTest, 0},
		{"process-pass", []string{"app.py", "--with", "pytest", "--python", "mypy", "--offline", "0"}, domain.StageProjectProcess, 0},
		{"process-fail", []string{"app.py", "--with", "pytest", "--python", "mypy", "--offline", "5"}, domain.StageProjectProcess, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			batches, uploadErr = nil, nil
			mu.Unlock()
			args := []string{"run", "--", uv, "run", "--offline", "--no-sync", "--no-project", "--no-python-downloads", "--python", python, "--", python}
			args = append(args, tc.args...)
			if code := Main(args); code != tc.exit {
				t.Fatalf("csx run returned %d, want %d", code, tc.exit)
			}
			if tc.stage == domain.StageProjectTest {
				if got, err := os.ReadFile(filepath.Join(proj, "test-ran")); err != nil || string(got) != "passed" {
					t.Fatalf("standard-library test did not run: %q, %v", got, err)
				}
			} else {
				raw, err := os.ReadFile(filepath.Join(proj, "argv.json"))
				if err != nil {
					t.Fatal(err)
				}
				var got []string
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, tc.args[1:]) {
					t.Errorf("program received %q, want %q", got, tc.args[1:])
				}
			}

			mu.Lock()
			got := append([]domain.ObservationBatch(nil), batches...)
			err := uploadErr
			mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			depRows, commandRows := 0, 0
			for _, batch := range got {
				isDep := batch.Package == dep.String()
				isCommand := strings.HasPrefix(batch.Package, "pkg:generic/cli/uv@")
				if !isDep && !isCommand {
					t.Errorf("unexpected package in upload: %s", batch.Package)
					continue
				}
				if isDep {
					depRows++
				} else {
					commandRows++
				}
				if tc.exit == 0 {
					wantStage := tc.stage
					if isCommand {
						wantStage = domain.StageProjectProcess
					}
					if batch.Stage != wantStage || batch.Result != domain.ResultPass {
						t.Errorf("success record = %+v, want %s PASS", batch, wantStage)
					}
				} else if batch.OuterStage != tc.stage || batch.ExitCode == nil || *batch.ExitCode != tc.exit || batch.TerminationKind != domain.TerminationExit || batch.Result != domain.ResultFail {
					t.Errorf("failure lost outer stage or exit result: %+v", batch)
				}
				if tc.stage == domain.StageProjectProcess && (batch.Stage == domain.StageProjectTest || batch.Stage == domain.StageProjectTypecheck || batch.OuterStage == domain.StageProjectTest || batch.OuterStage == domain.StageProjectTypecheck) {
					t.Errorf("program arguments invented a check record: %+v", batch)
				}
			}
			if depRows == 0 || commandRows == 0 {
				t.Errorf("missing dependency or uv command evidence: deps=%d, commands=%d", depRows, commandRows)
			}
		})
	}
}
