package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func findUse(uses []scanner.SymbolUsage, pkg, family string) (scanner.SymbolUsage, bool) {
	for _, u := range uses {
		if u.Package.Name == pkg && u.Family == family {
			return u, true
		}
	}
	return scanner.SymbolUsage{}, false
}

// Reproduces the large ignored state tree and a source tree that exceeds the
// bounded symbol scan. Both markers were incorrectly observed on the baseline.
func TestScanSymbolsBoundedGitignore(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("state/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "state"), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("000-first.ts", "import axios from 'axios'; axios.first()")
	for i := 0; i < 702; i++ {
		write(filepath.Join("state", fmt.Sprintf("%04d.js", i)), "import axios from 'axios'; axios.ignored()\n"+strings.Repeat("x", 24*1024))
	}
	for i := 0; i < 300; i++ {
		write(fmt.Sprintf("%04d.ts", i+1), "import axios from 'axios'; axios.middle()\n"+strings.Repeat("x", 8*1024))
	}
	write("zz-last.ts", "import axios from 'axios'; axios.beyondBudget()")
	pkgs := []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.0.0"}}}
	uses, err := (Adapter{}).ScanSymbols(context.Background(), dir, pkgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findUse(uses, "axios", "axios.first"); !ok {
		t.Fatal("first source was not scanned")
	}
	if _, ok := findUse(uses, "axios", "axios.ignored"); ok {
		t.Fatal("gitignored state source was scanned")
	}
	if _, ok := findUse(uses, "axios", "axios.beyondBudget"); ok {
		t.Fatal("scan exceeded source budget")
	}
}

func TestScanSymbols(t *testing.T) {
	a := Adapter{}
	ctx := context.Background()
	pkgs, err := a.ScanPackages(ctx, "testdata/npmproj")
	if err != nil {
		t.Fatalf("ScanPackages: %v", err)
	}
	uses, err := a.ScanSymbols(ctx, "testdata/npmproj", pkgs)
	if err != nil {
		t.Fatalf("ScanSymbols: %v", err)
	}

	post, ok := findUse(uses, "axios", "axios.post")
	if !ok {
		t.Fatalf("axios.post not found in %v", uses)
	}
	if post.Confidence != domain.SymbolProbable {
		t.Errorf("axios.post confidence = %q, want PROBABLE", post.Confidence)
	}
	if post.Kind != "method" {
		t.Errorf("axios.post kind = %q, want method", post.Kind)
	}
	if got := post.Package.String(); got != "pkg:npm/axios@1.12.0" {
		t.Errorf("axios.post package = %q, want pkg:npm/axios@1.12.0", got)
	}

	if u, ok := findUse(uses, "axios", "axios.get"); !ok {
		t.Error("axios.get (tagged template member) not found")
	} else if u.Confidence != domain.SymbolProbable {
		t.Errorf("axios.get confidence = %q, want PROBABLE", u.Confidence)
	}

	if u, ok := findUse(uses, "axios", "axios.isAxiosError"); !ok {
		t.Error("axios.isAxiosError (aliased named import, used) not found")
	} else if u.Confidence != domain.SymbolProbable {
		t.Errorf("axios.isAxiosError confidence = %q, want PROBABLE", u.Confidence)
	}

	if _, ok := findUse(uses, "axios", "axios.AxiosError"); !ok {
		t.Error("axios.AxiosError (named import, used) not found")
	}

	if u, ok := findUse(uses, "follow-redirects", "follow-redirects.request"); !ok {
		t.Error("follow-redirects.request (subpath import binding) not found")
	} else if got := u.Package.String(); got != "pkg:npm/follow-redirects@1.15.6" {
		t.Errorf("subpath import mapped to %q, want pkg:npm/follow-redirects@1.15.6", got)
	}

	if _, ok := findUse(uses, "follow-redirects", "follow-redirects.wrap"); !ok {
		t.Error("follow-redirects.wrap (require binding member) not found")
	}

	if u, ok := findUse(uses, "axios", ""); !ok {
		t.Error("package-level axios usage (bare dynamic import) not found")
	} else if u.Confidence != domain.SymbolUnknown {
		t.Errorf("package-level confidence = %q, want UNKNOWN", u.Confidence)
	}

	for _, u := range uses {
		if strings.Contains(u.Family, "SHOULD_NOT_APPEAR") {
			t.Errorf("node_modules was scanned: %v", u)
		}
		if strings.Contains(u.Family, "neverSeen") {
			t.Errorf("dist was scanned: %v", u)
		}
	}
}

func TestScanSymbolsNoPackages(t *testing.T) {
	a := Adapter{}
	uses, err := a.ScanSymbols(context.Background(), "testdata/npmproj", nil)
	if err != nil {
		t.Fatalf("ScanSymbols: %v", err)
	}
	if len(uses) != 0 {
		t.Errorf("expected no usages without packages, got %v", uses)
	}
}
