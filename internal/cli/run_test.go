package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
