package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
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
