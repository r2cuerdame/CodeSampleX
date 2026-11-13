package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// writeProject creates a minimal npm project with a lockfile-resolved
// public dependency and a private file: dependency.
func writeProject(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", `{"name":"p","version":"1.0.0","type":"module",
		"dependencies":{"axios":"^1.12.0","local-lib":"file:../local-lib"}}`)
	write("package-lock.json", `{"name":"p","lockfileVersion":3,"packages":{
		"":{"name":"p","dependencies":{"axios":"^1.12.0","local-lib":"file:../local-lib"}},
		"node_modules/axios":{"version":"1.12.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz"},
		"node_modules/local-lib":{"resolved":"../local-lib","link":true}}}`)
	if err := os.WriteFile(filepath.Join(dir, "src", "index.js"),
		[]byte("import axios from 'axios';\nexport const post = axios.post;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scanHome points CSX_HOME at a throwaway dir in community mode so the
// recorder writes somewhere disposable. No network: publicness stays
// UNKNOWN, which is the safe default and exercises the exclusion path.
func scanHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = "http://127.0.0.1:1" // unreachable on purpose
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestScanRecordsUsedEvidence(t *testing.T) {
	home := scanHome(t)
	proj := filepath.Join(t.TempDir(), "proj")
	writeProject(t, proj)

	if code := Main([]string{"scan", proj}); code != 0 {
		t.Fatalf("csx scan returned %d", code)
	}

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	pkgs, err := db.ListPackages(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sawAxios, sawPrivate bool
	for _, p := range pkgs {
		switch {
		case strings.Contains(p.PURL.String(), "axios"):
			sawAxios = true
		case strings.Contains(p.PURL.String(), "local-lib"):
			sawPrivate = true
			if p.Publicness != "PRIVATE" {
				t.Errorf("file: dependency recorded as %s, want PRIVATE", p.Publicness)
			}
		}
	}
	if !sawAxios {
		t.Error("scan did not record the lockfile-resolved public dependency")
	}
	if !sawPrivate {
		t.Error("scan did not inventory the private dependency locally")
	}
}

// A scan proves only that packages are in use. It must never leave a
// build/test stage behind, because nothing was built.
func TestScanRecordsNothingStrongerThanUsed(t *testing.T) {
	home := scanHome(t)
	proj := filepath.Join(t.TempDir(), "proj")
	writeProject(t, proj)

	if code := Main([]string{"scan", proj}); code != 0 {
		t.Fatal("scan failed")
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.PendingObservations(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Stage != "USED" {
			t.Errorf("scan recorded stage %q; a scan builds nothing and may only claim USED", r.Stage)
		}
		if r.Result != "PASS" {
			t.Errorf("USED row result = %q, want PASS", r.Result)
		}
	}
}

func TestScanRecursiveFindsNestedProjects(t *testing.T) {
	scanHome(t)
	root := t.TempDir()
	writeProject(t, filepath.Join(root, "a"))
	writeProject(t, filepath.Join(root, "nested", "b"))
	// A dependency copy inside a project must not be scanned as its own
	// project: its versions already come from the owner's lockfile.
	writeProject(t, filepath.Join(root, "a", "node_modules", "vendored"))

	out, code := captureStdout(t, func() int {
		return Main([]string{"scan", root, "--recursive", "--dry-run"})
	})
	if code != 0 {
		t.Fatalf("recursive scan returned %d:\n%s", code, out)
	}
	if !strings.Contains(out, "2 project(s)") {
		t.Errorf("expected exactly the two real projects:\n%s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("scan descended into node_modules:\n%s", out)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("--dry-run must say it recorded nothing:\n%s", out)
	}
}

func TestScanDryRunWritesNothing(t *testing.T) {
	home := scanHome(t)
	proj := filepath.Join(t.TempDir(), "proj")
	writeProject(t, proj)

	if _, code := captureStdout(t, func() int {
		return Main([]string{"scan", proj, "--dry-run"})
	}); code != 0 {
		t.Fatalf("dry run returned %d", code)
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.PendingObservations(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("--dry-run recorded %d observations, want 0", len(rows))
	}
}

func TestScanBunLockEvidence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	var mu sync.Mutex
	var uploaded []domain.ObservationBatch
	var srvErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			mu.Lock()
			srvErr = fmt.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			mu.Unlock()
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		err := json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		if err != nil && srvErr == nil {
			srvErr = err
		} else {
			uploaded = append(uploaded, payload.Batches...)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.DaemonPort = 0
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}

	type seededPkg struct {
		name    string
		version string
		direct  bool
	}
	seeded := []seededPkg{
		{"axios", "1.12.0", true},
		{"axios", "0.27.2", false},
		{"@types/node", "22.5.4", true},
		{"real", "2.0.0", true},
		{"transitive", "1.0.0", false},
		{"local", "1.0.0", false},
	}
	wantDirect := make(map[string]bool, len(seeded))

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, p := range seeded {
		purl := domain.PURL{Ecosystem: "npm", Name: p.name, Version: p.version}
		if p.name != "local" {
			wantDirect[purl.String()] = p.direct
		}
		if err := db.SetPublicness(context.Background(), purl, "PUBLIC"); err != nil {
			db.Close()
			t.Fatalf("seed publicness %s: %v", purl, err)
		}
	}
	db.Close()

	proj := t.TempDir()
	lock := `{
 "lockfileVersion": 1,
 "workspaces": {"": {"dependencies": {"axios": "^1.12.0", "alias": "npm:real@2.0.0", "local": "file:../local"}, "devDependencies": {"@types/node": "^22.5.4"}}},
 "packages": {
  "axios": ["axios@1.12.0", "", {}, ""],
  "parent/axios": ["axios@0.27.2", "", {}, ""],
  "duplicate/axios": ["axios@1.12.0", "", {}, ""],
  "@types/node": ["@types/node@22.5.4", "", {}, ""],
  "alias": ["real@2.0.0", "", {}, ""],
  "transitive": ["transitive@1.0.0", "", {}, ""],
  "local": ["local@1.0.0", "", {}, ""],
  "workspace": ["workspace@workspace:packages/workspace"],
  "file": ["file@file:../private"],
  "link": ["link@link:../private"],
  "git": ["git@git+ssh://git@example.com/private.git"],
 }
}`
	if err := os.WriteFile(filepath.Join(proj, "package.json"), []byte(`{"name":"bun-scan-test","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "bun.lock"), []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}

	dryOut, dryCode := captureStdout(t, func() int {
		return Main([]string{"scan", proj, "--dry-run"})
	})
	if dryCode != 0 {
		t.Fatalf("csx scan --dry-run returned %d:\n%s", dryCode, dryOut)
	}
	if !strings.Contains(dryOut, "dry run") {
		t.Errorf("--dry-run output missing marker:\n%s", dryOut)
	}
	db, err = localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	dryRows, err := db.PendingObservations(context.Background(), 100)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRows) != 0 {
		t.Fatalf("--dry-run recorded %d observations, want 0", len(dryRows))
	}

	scanOut, scanCode := captureStdout(t, func() int {
		return Main([]string{"scan", proj})
	})
	if scanCode != 0 {
		t.Fatalf("csx scan returned %d:\n%s", scanCode, scanOut)
	}

	db, err = localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.PendingObservations(context.Background(), 100)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("csx scan recorded 0 pending observations")
	}
	if len(rows) != len(wantDirect) {
		t.Fatalf("pending observations = %d, want %d: %+v", len(rows), len(wantDirect), rows)
	}
	seenRows := make(map[string]bool, len(rows))
	for _, r := range rows {
		want, ok := wantDirect[r.PURL]
		if !ok {
			t.Errorf("unexpected observation package %q: %+v", r.PURL, r)
			continue
		}
		seenRows[r.PURL] = true
		if r.Stage != domain.StageUsed || r.Result != domain.ResultPass {
			t.Errorf("%s: got stage=%q result=%q, want USED PASS", r.PURL, r.Stage, r.Result)
		}
		if r.Direct != want {
			t.Errorf("%s: pending row Direct = %v, want %v", r.PURL, r.Direct, want)
		}
	}
	if len(seenRows) != len(wantDirect) {
		t.Errorf("saw %d distinct packages in pending rows, want %d", len(seenRows), len(wantDirect))
	}

	syncOut, syncCode := captureStdout(t, func() int {
		return Main([]string{"sync", "--uploads-only"})
	})
	if syncCode != 0 {
		t.Fatalf("csx sync --uploads-only returned %d:\n%s", syncCode, syncOut)
	}

	mu.Lock()
	gotBatches := append([]domain.ObservationBatch(nil), uploaded...)
	gotErr := srvErr
	mu.Unlock()
	if gotErr != nil {
		t.Fatalf("receiver server error: %v", gotErr)
	}
	if len(gotBatches) != len(wantDirect) {
		t.Fatalf("uploaded %d batches, want %d: %+v", len(gotBatches), len(wantDirect), gotBatches)
	}
	for _, batch := range gotBatches {
		want, ok := wantDirect[batch.Package]
		if !ok {
			t.Errorf("unexpected uploaded package %q: %+v", batch.Package, batch)
			continue
		}
		if batch.Stage != domain.StageUsed || batch.Result != domain.ResultPass {
			t.Errorf("%s: uploaded stage=%q result=%q, want USED PASS", batch.Package, batch.Stage, batch.Result)
		}
		if batch.Direct != want {
			t.Errorf("%s: uploaded batch Direct = %v, want %v", batch.Package, batch.Direct, want)
		}
	}
}

func TestScanPythonCommentedDependenciesDirectEvidence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)

	var mu sync.Mutex
	var uploaded []domain.ObservationBatch
	var srvErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			mu.Lock()
			srvErr = fmt.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			mu.Unlock()
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		err := json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		if err != nil && srvErr == nil {
			srvErr = err
		} else {
			uploaded = append(uploaded, payload.Batches...)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.DaemonPort = 0
	cfg.ServerURL = srv.URL
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}

	type seededPkg struct {
		name    string
		version string
		direct  bool
	}
	seeded := []seededPkg{
		{"fastapi", "0.116.0", true},
		{"requests", "2.32.3", true},
		{"extra-pkg", "2.0.0", true},
		{"commented-line", "1.0.0", false},
		{"commented-inline", "1.5.0", false},
		{"commented-opt", "2.5.0", false},
	}
	wantDirect := make(map[string]bool, len(seeded))

	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	for _, p := range seeded {
		purl := domain.PURL{Ecosystem: "pypi", Name: p.name, Version: p.version}
		wantDirect[purl.String()] = p.direct
		if err := db.SetPublicness(context.Background(), purl, "PUBLIC"); err != nil {
			db.Close()
			t.Fatalf("seed publicness %s: %v", purl, err)
		}
	}
	db.Close()

	proj := t.TempDir()
	pyproject := `[project]
name = "scanproj"
version = "0.1.0"
dependencies = [
    # "commented-line>=1.0.0",
    "fastapi>=0.116.0", # "commented-inline>=1.5.0" ]
    "requests[security]>=2.32.0; extra == \"#not-comment]\" and sys_platform == \"win32\"",
]

[project.optional-dependencies]
dev = [
    # "commented-opt>=2.5.0", # ]
    "Extra_Pkg>=2.0.0",
]
`
	var uvLock strings.Builder
	uvLock.WriteString("version = 1\n\n[[package]]\nname = \"scanproj\"\nversion = \"0.1.0\"\nsource = { editable = \".\" }\n")
	for _, p := range seeded {
		fmt.Fprintf(&uvLock, "\n[[package]]\nname = %q\nversion = %q\nsource = { registry = \"https://pypi.org/simple\" }\n", p.name, p.version)
	}
	if err := os.WriteFile(filepath.Join(proj, "pyproject.toml"), []byte(pyproject), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "uv.lock"), []byte(uvLock.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	dryOut, dryCode := captureStdout(t, func() int {
		return Main([]string{"scan", proj, "--dry-run"})
	})
	if dryCode != 0 {
		t.Fatalf("csx scan --dry-run returned %d:\n%s", dryCode, dryOut)
	}
	if !strings.Contains(dryOut, "dry run") {
		t.Errorf("--dry-run output missing marker:\n%s", dryOut)
	}
	db, err = localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	dryRows, err := db.PendingObservations(context.Background(), 100)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(dryRows) != 0 {
		t.Fatalf("--dry-run recorded %d observations, want 0", len(dryRows))
	}

	scanOut, scanCode := captureStdout(t, func() int {
		return Main([]string{"scan", proj})
	})
	if scanCode != 0 {
		t.Fatalf("csx scan returned %d:\n%s", scanCode, scanOut)
	}

	db, err = localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.PendingObservations(context.Background(), 100)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("csx scan recorded 0 pending observations")
	}
	if len(rows) != len(wantDirect) {
		t.Fatalf("pending observations = %d, want %d: %+v", len(rows), len(wantDirect), rows)
	}
	seenRows := make(map[string]bool, len(rows))
	for _, r := range rows {
		want, ok := wantDirect[r.PURL]
		if !ok {
			t.Errorf("unexpected observation package %q: %+v", r.PURL, r)
			continue
		}
		seenRows[r.PURL] = true
		if r.Stage != domain.StageUsed || r.Result != domain.ResultPass {
			t.Errorf("%s: got stage=%q result=%q, want USED PASS", r.PURL, r.Stage, r.Result)
		}
		if r.Direct != want {
			t.Errorf("%s: pending row Direct = %v, want %v", r.PURL, r.Direct, want)
		}
	}
	if len(seenRows) != len(wantDirect) {
		t.Errorf("saw %d distinct packages in pending rows, want %d", len(seenRows), len(wantDirect))
	}

	syncOut, syncCode := captureStdout(t, func() int {
		return Main([]string{"sync", "--uploads-only"})
	})
	if syncCode != 0 {
		t.Fatalf("csx sync --uploads-only returned %d:\n%s", syncCode, syncOut)
	}

	mu.Lock()
	gotBatches := append([]domain.ObservationBatch(nil), uploaded...)
	gotErr := srvErr
	mu.Unlock()
	if gotErr != nil {
		t.Fatalf("receiver server error: %v", gotErr)
	}
	if len(gotBatches) != len(wantDirect) {
		t.Fatalf("uploaded %d batches, want %d: %+v", len(gotBatches), len(wantDirect), gotBatches)
	}
	for _, batch := range gotBatches {
		want, ok := wantDirect[batch.Package]
		if !ok {
			t.Errorf("unexpected uploaded package %q: %+v", batch.Package, batch)
			continue
		}
		if batch.Stage != domain.StageUsed || batch.Result != domain.ResultPass {
			t.Errorf("%s: uploaded stage=%q result=%q, want USED PASS", batch.Package, batch.Stage, batch.Result)
		}
		if batch.Direct != want {
			t.Errorf("%s: uploaded batch Direct = %v, want %v", batch.Package, batch.Direct, want)
		}
	}
}
