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
	"github.com/r2cuerdame/codesamplex/internal/evidence"
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

func TestScanNodeLockfileAliasesDirectEvidence(t *testing.T) {
	type seededPkg struct {
		name    string
		version string
		direct  bool
	}
	publicSeeded := []seededPkg{
		{"lodash-es", "4.17.21", true},
		{"lodash-es", "4.17.15", false},
		{"@scope/pkg", "2.0.1", true},
		{"chalk", "5.3.0", true},
		{"@npm/types", "1.2.0", true},
		{"string-width", "4.2.3", false},
	}
	forbiddenAliases := map[string]bool{
		"lodash":           true,
		"@my/alias":        true,
		"no-range":         true,
		"string-width-cjs": true,
	}

	const aliasPackageJSON = `{
  "name": "alias-scan-proj",
  "version": "1.0.0",
  "dependencies": {
    "lodash": "npm:lodash-es@^4.17.21",
    "@my/alias": "npm:@scope/pkg@^2.0.0",
    "no-range": "npm:chalk",
    "@npm/types": "^1.2.0",
    "local-lib": "file:../local-lib"
  }
}
`

	cases := []struct {
		name     string
		lockFile string
		lockBody string
	}{
		{
			name:     "pnpm-lock.yaml",
			lockFile: "pnpm-lock.yaml",
			lockBody: `lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      lodash:
        specifier: npm:lodash-es@^4.17.21
        version: lodash-es@4.17.21
      '@my/alias':
        specifier: 'npm:@scope/pkg@^2.0.0'
        version: '@scope/pkg@2.0.1(@scope/react@18.3.1)'
      no-range:
        specifier: npm:chalk
        version: npm:chalk@5.3.0
      '@npm/types':
        specifier: ^1.2.0
        version: 1.2.0
      local-lib:
        specifier: file:../local-lib
        version: file:../local-lib

packages:

  lodash-es@4.17.21:
    resolution: {integrity: sha512-fixture1}

  lodash-es@4.17.15:
    resolution: {integrity: sha512-fixture2}

  '@scope/pkg@2.0.1':
    resolution: {integrity: sha512-fixture3}

  chalk@5.3.0:
    resolution: {integrity: sha512-fixture4}

  '@npm/types@1.2.0':
    resolution: {integrity: sha512-fixture5}

  string-width@4.2.3:
    resolution: {integrity: sha512-fixture6}

  local-lib@file:../local-lib:
    resolution: {directory: ../local-lib, type: directory}
    version: 1.0.0

snapshots:

  cliui@8.0.2:
    dependencies:
      string-width-cjs: string-width@4.2.3
`,
		},
		{
			name:     "yarn.lock",
			lockFile: "yarn.lock",
			lockBody: `# yarn lockfile v1

"lodash@npm:lodash-es@^4.17.21":
  version "4.17.21"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.21.tgz#f1"

lodash-es@^4.17.0:
  version "4.17.15"
  resolved "https://registry.yarnpkg.com/lodash-es/-/lodash-es-4.17.15.tgz#f2"

"@my/alias@npm:@scope/pkg@^2.0.0":
  version "2.0.1"
  resolved "https://registry.yarnpkg.com/@scope/pkg/-/pkg-2.0.1.tgz#f3"

"no-range@npm:chalk":
  version "5.3.0"
  resolved "https://registry.yarnpkg.com/chalk/-/chalk-5.3.0.tgz#f4"

"@npm/types@^1.2.0":
  version "1.2.0"
  resolved "https://registry.yarnpkg.com/@npm/types/-/types-1.2.0.tgz#f5"

"string-width-cjs@npm:string-width@^4.2.0":
  version "4.2.3"
  resolved "https://registry.yarnpkg.com/string-width/-/string-width-4.2.3.tgz#f6"

"local-lib@file:../local-lib":
  version "1.0.0"
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			wantDirect := make(map[string]bool, len(publicSeeded))
			db, err := localdb.Open(filepath.Join(home, "csx.db"))
			if err != nil {
				t.Fatalf("open db: %v", err)
			}
			for _, p := range publicSeeded {
				purl := domain.PURL{Ecosystem: "npm", Name: p.name, Version: p.version}
				wantDirect[purl.String()] = p.direct
				if err := db.SetPublicness(context.Background(), purl, "PUBLIC"); err != nil {
					db.Close()
					t.Fatalf("seed publicness %s: %v", purl, err)
				}
			}
			db.Close()

			proj := t.TempDir()
			if err := os.WriteFile(filepath.Join(proj, "package.json"), []byte(aliasPackageJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proj, tc.lockFile), []byte(tc.lockBody), 0o600); err != nil {
				t.Fatal(err)
			}

			searchScan, err := evidence.Scan(context.Background(), proj, nil)
			if err != nil {
				t.Fatalf("evidence.Scan: %v", err)
			}
			ctxPkgs := projectPackages(searchScan)
			if len(ctxPkgs) != len(wantDirect) {
				t.Fatalf("projectPackages = %v, want %d packages", ctxPkgs, len(wantDirect))
			}
			for i, purl := range ctxPkgs {
				want, ok := wantDirect[purl]
				if !ok {
					t.Errorf("unexpected search context package %q in %v", purl, ctxPkgs)
					continue
				}
				if i < 4 && !want {
					t.Errorf("expected direct packages first in projectPackages, got %q at index %d: %v", purl, i, ctxPkgs)
				}
				if i >= 4 && want {
					t.Errorf("expected indirect packages after direct in projectPackages, got %q at index %d: %v", purl, i, ctxPkgs)
				}
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
			if !strings.Contains(dryOut, "6 public · 1 private · 0 unknown") {
				t.Errorf("--dry-run counts mismatch:\n%s", dryOut)
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
			inv, err := db.ListPackages(context.Background())
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			rows, err := db.PendingObservations(context.Background(), 100)
			db.Close()
			if err != nil {
				t.Fatal(err)
			}

			privatePURL := (domain.PURL{Ecosystem: "npm", Name: "local-lib", Version: "1.0.0"}).String()
			if len(inv) != len(wantDirect)+1 {
				t.Fatalf("local package inventory = %d, want %d: %+v", len(inv), len(wantDirect)+1, inv)
			}
			for _, p := range inv {
				if forbiddenAliases[p.PURL.Name] || strings.Contains(p.PURL.Name, "@npm:") ||
					strings.HasPrefix(p.PURL.Name, "npm:") || strings.Contains(p.PURL.Version, "@") {
					t.Errorf("alias or corrupted package leaked into local inventory: %+v", p)
				}
				if p.PURL.String() == privatePURL {
					if p.Publicness != "PRIVATE" {
						t.Errorf("local-lib publicness = %q, want PRIVATE", p.Publicness)
					}
					continue
				}
				if _, ok := wantDirect[p.PURL.String()]; !ok {
					t.Errorf("unexpected package in local inventory: %+v", p)
				}
			}

			if len(rows) != len(wantDirect) {
				t.Fatalf("pending observations = %d, want %d: %+v", len(rows), len(wantDirect), rows)
			}
			seenRows := make(map[string]bool, len(rows))
			for _, r := range rows {
				if strings.Contains(r.PURL, "npm:") {
					t.Errorf("corrupted package PURL %q in pending rows: %+v", r.PURL, r)
				}
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
			seenUploaded := make(map[string]bool, len(gotBatches))
			for _, batch := range gotBatches {
				if strings.Contains(batch.Package, "npm:") {
					t.Errorf("corrupted uploaded package %q: %+v", batch.Package, batch)
				}
				want, ok := wantDirect[batch.Package]
				if !ok {
					t.Errorf("unexpected uploaded package %q: %+v", batch.Package, batch)
					continue
				}
				seenUploaded[batch.Package] = true
				if batch.Stage != domain.StageUsed || batch.Result != domain.ResultPass {
					t.Errorf("%s: uploaded stage=%q result=%q, want USED PASS", batch.Package, batch.Stage, batch.Result)
				}
				if batch.Direct != want {
					t.Errorf("%s: uploaded batch Direct = %v, want %v", batch.Package, batch.Direct, want)
				}
			}
			if len(seenUploaded) != len(wantDirect) {
				t.Errorf("saw %d distinct uploaded packages, want %d", len(seenUploaded), len(wantDirect))
			}
		})
	}
}
