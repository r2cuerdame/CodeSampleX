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
	"runtime"
	"slices"
	"strconv"
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

// uvStandInEnv and turboStandInEnv turn this test binary into a stand-in
// executable (see TestMain). The stand-in records the arguments csx handed it
// and exits with the code the test asks for, so the csx run path is covered
// without installing external tools.
const (
	uvStandInEnv    = "CSX_TEST_UV_STANDIN"
	turboStandInEnv = "CSX_TEST_TURBO_STANDIN"
)

func runUVStandIn() int {
	return runCommandStandIn(uvStandInEnv, "uv 0.9.0\n")
}

func runTurboStandIn() int {
	return runCommandStandIn(turboStandInEnv, "")
}

func runCommandStandIn(envVar, versionBanner string) int {
	args := os.Args[1:]
	if versionBanner != "" && len(args) == 1 && args[0] == "--version" {
		os.Stdout.WriteString(versionBanner)
		return 0
	}
	raw, err := json.Marshal(args)
	if err == nil {
		err = os.WriteFile(os.Getenv(envVar), raw, 0o600)
	}
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		return 99
	}
	code, err := strconv.Atoi(os.Getenv(envVar + "_EXIT"))
	if err != nil {
		return 98
	}
	return code
}

func installStandInBinary(t *testing.T, name, envVar string) (exePath, argvFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	exePath = filepath.Join(bin, name)
	if runtime.GOOS == "windows" {
		exePath += ".exe"
	}
	if err := os.WriteFile(exePath, src, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	argvFile = filepath.Join(t.TempDir(), "argv.json")
	t.Setenv(envVar, argvFile)
	return exePath, argvFile
}

type standInEvidenceReceiver struct {
	mu        sync.Mutex
	batches   []domain.ObservationBatch
	uploadErr error
}

func newStandInEvidenceReceiver(t *testing.T, publicDeps ...domain.PURL) *standInEvidenceReceiver {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	rec := &standInEvidenceReceiver{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		err := json.NewDecoder(r.Body).Decode(&payload)
		rec.mu.Lock()
		if err != nil {
			rec.uploadErr = err
		} else {
			rec.batches = append(rec.batches, payload.Batches...)
		}
		rec.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

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
	for _, dep := range publicDeps {
		if err := db.SetPublicness(context.Background(), dep, "PUBLIC"); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return rec
}

func (r *standInEvidenceReceiver) reset() {
	r.mu.Lock()
	r.batches, r.uploadErr = nil, nil
	r.mu.Unlock()
}

func (r *standInEvidenceReceiver) drain(t *testing.T) []domain.ObservationBatch {
	t.Helper()
	r.mu.Lock()
	rows := append([]domain.ObservationBatch(nil), r.batches...)
	err := r.uploadErr
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func readStandInArgv(t *testing.T, argvFile string) []string {
	t.Helper()
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestRunUVPreparationOptionsStandIn is the isolated counterpart of
// TestRunUVPreparationOptions: a stand-in uv replaces the real tool, so it
// proves classification, argument passthrough and exit codes through csx run
// but not that uv itself accepts these options.
func TestRunUVPreparationOptionsStandIn(t *testing.T) {
	uv, argvFile := installStandInBinary(t, "uv", uvStandInEnv)
	dep := domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"}
	receiver := newStandInEvidenceReceiver(t, dep)

	proj := t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "requirements.txt"), []byte("requests==2.32.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(proj)

	for _, tc := range []struct {
		name  string
		args  []string
		stage domain.Stage // "" when the call cannot be classified
		exit  int
	}{
		{"unittest", []string{"--offline", "python", "-m", "unittest"}, domain.StageProjectTest, 0},
		{"mypy", []string{"--python", "3.12", "--with", "pytest", "--", "mypy", "."}, domain.StageProjectTypecheck, 0},
		{"pytest-fail", []string{"--no-sync", "pytest"}, domain.StageProjectTest, 5},
		{"process-pass", []string{"--no-project", "app.py", "--with", "pytest", "--python", "mypy"}, domain.StageProjectProcess, 0},
		{"process-fail", []string{"--no-project", "app.py", "--with", "pytest", "--python", "mypy"}, domain.StageProjectProcess, 5},
		{"unsupported-option", []string{"--directory", "pytest", "mypy"}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver.reset()
			os.Remove(argvFile)
			t.Setenv(uvStandInEnv+"_EXIT", strconv.Itoa(tc.exit))
			uvArgs := append([]string{"run"}, tc.args...)
			if code := Main(append([]string{"run", "--", uv}, uvArgs...)); code != tc.exit {
				t.Fatalf("csx run returned %d, want %d", code, tc.exit)
			}
			got := readStandInArgv(t, argvFile)
			if !slices.Equal(got, uvArgs) {
				t.Errorf("uv received %q, want %q", got, uvArgs)
			}

			rows := receiver.drain(t)
			depRows := 0
			for _, batch := range rows {
				isCheck := batch.Stage == domain.StageProjectTest || batch.Stage == domain.StageProjectTypecheck || batch.OuterStage == domain.StageProjectTest || batch.OuterStage == domain.StageProjectTypecheck
				if isCheck && tc.stage != domain.StageProjectTest && tc.stage != domain.StageProjectTypecheck {
					t.Errorf("invented a check record: %+v", batch)
				}
				if batch.Package != dep.String() {
					continue
				}
				depRows++
				if tc.stage == "" {
					continue
				}
				if tc.exit == 0 {
					if batch.Stage != tc.stage || batch.Result != domain.ResultPass {
						t.Errorf("success record = %+v, want %s PASS", batch, tc.stage)
					}
				} else if batch.OuterStage != tc.stage || batch.ExitCode == nil || *batch.ExitCode != tc.exit || batch.TerminationKind != domain.TerminationExit || batch.Result != domain.ResultFail {
					t.Errorf("failure lost outer stage or exit result: %+v", batch)
				}
			}
			if tc.stage != "" && depRows == 0 {
				t.Errorf("no dependency evidence uploaded: %+v", rows)
			}
		})
	}
}

// TestRunTurboClassificationStandIn exercises `csx run -- turbo ...` end-to-end
// with a stand-in turbo binary and seeded local cache so filter/package task
// classification, exit code passthrough, exact argument passthrough, and
// non-executing or ambiguous invocations (recorded as USED only) are verified
// without installing turbo or contacting external registries.
func TestRunTurboClassificationStandIn(t *testing.T) {
	turboExe, argvFile := installStandInBinary(t, "turbo", turboStandInEnv)
	resolved, err := exec.LookPath("turbo")
	if err != nil {
		t.Fatalf("LookPath(turbo) failed: %v", err)
	}
	wantInfo, err := os.Stat(turboExe)
	if err != nil {
		t.Fatal(err)
	}
	gotInfo, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("LookPath(turbo) = %q, want stand-in %q", resolved, turboExe)
	}

	dep := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	receiver := newStandInEvidenceReceiver(t, dep)

	proj := t.TempDir()
	files := map[string]string{
		"package.json": `{"name":"mono","private":true,"dependencies":{"axios":"^1.12.0","privlib":"file:../privlib"}}`,
		"package-lock.json": `{
  "name": "mono",
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

	for _, tc := range []struct {
		name  string
		args  []string
		stage domain.Stage // "" when turbo invocation must not emit a check stage
		exit  int
	}{
		{"filter-test-pass", []string{"--filter=web", "test"}, domain.StageProjectTest, 0},
		{"pkg-build-pass", []string{"-F", "web", "run", "web#build"}, domain.StageProjectCompile, 0},
		{"pkg-test-fail", []string{"run", "@repo/ui#test", "--", "src/a.test.ts"}, domain.StageProjectTest, 5},
		{"cwd-build-fail", []string{"--cwd", "apps/web", "build"}, domain.StageProjectCompile, 3},
		{"dry-run-build", []string{"run", "build", "--dry-run"}, "", 0},
		{"dry-json-test", []string{"run", "test", "--dry=json"}, "", 0},
		{"filter-value-collision", []string{"-F", "test", "dev"}, "", 0},
		{"boundary-dev-before-test", []string{"run", "dev", "--", "test"}, "", 0},
		{"non-exec-prune", []string{"prune", "test"}, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receiver.reset()
			os.Remove(argvFile)
			t.Setenv(turboStandInEnv+"_EXIT", strconv.Itoa(tc.exit))
			if code := Main(append([]string{"run", "--", "turbo"}, tc.args...)); code != tc.exit {
				t.Fatalf("csx run returned %d, want %d", code, tc.exit)
			}
			got := readStandInArgv(t, argvFile)
			if !slices.Equal(got, tc.args) {
				t.Errorf("turbo received %q, want %q", got, tc.args)
			}

			rows := receiver.drain(t)
			depRows, usedRows := 0, 0
			for _, batch := range rows {
				if batch.Package != dep.String() {
					t.Errorf("unexpected package in upload: %+v", batch)
					continue
				}
				isCheck := batch.Stage == domain.StageProjectTest || batch.Stage == domain.StageProjectCompile || batch.Stage == domain.StageProjectTypecheck ||
					batch.OuterStage == domain.StageProjectTest || batch.OuterStage == domain.StageProjectCompile || batch.OuterStage == domain.StageProjectTypecheck
				if tc.stage == "" {
					if isCheck {
						t.Errorf("invented a check record: %+v", batch)
					}
					if batch.Stage != domain.StageUsed || batch.Result != domain.ResultPass {
						t.Errorf("unclassified run = %+v, want USED PASS", batch)
					}
					usedRows++
					continue
				}
				depRows++
				if tc.exit == 0 {
					if batch.Stage != tc.stage || batch.Result != domain.ResultPass {
						t.Errorf("success record = %+v, want %s PASS", batch, tc.stage)
					}
				} else if batch.OuterStage != tc.stage || batch.ExitCode == nil || *batch.ExitCode != tc.exit || batch.TerminationKind != domain.TerminationExit || batch.Result != domain.ResultFail {
					t.Errorf("failure lost outer stage or exit result: %+v", batch)
				}
			}
			if tc.stage == "" && usedRows == 0 {
				t.Errorf("no USED evidence uploaded: %+v", rows)
			}
			if tc.stage != "" && depRows == 0 {
				t.Errorf("no classified dependency evidence uploaded: %+v", rows)
			}
		})
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
