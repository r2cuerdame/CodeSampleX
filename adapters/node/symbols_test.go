package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func scanSymbolsWithTimeout(t *testing.T, dir string, pkgs []scanner.ResolvedPackage) ([]scanner.SymbolUsage, error) {
	t.Helper()
	type result struct {
		uses []scanner.SymbolUsage
		err  error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		uses, err := (Adapter{}).ScanSymbols(ctx, dir, pkgs)
		done <- result{uses: uses, err: err}
	}()
	select {
	case res := <-done:
		return res.uses, res.err
	case <-time.After(3 * time.Second):
		t.Fatalf("ScanSymbols(%q) did not complete within 3s", dir)
		return nil, nil
	}
}

func TestScanSymbolsNonCanonicalDirPaths(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)

	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	write := func(rel, content string) {
		t.Helper()
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "state/\n")
	write("src/first.ts", "import axios from 'axios'; axios.first();\n")
	write("state/ignored.ts", "import axios from 'axios'; axios.ignored();\n")

	pkgs := []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.0.0"}}}
	sep := string(filepath.Separator)
	variants := []struct {
		name string
		dir  string
	}{
		{name: "trailing-separator", dir: dir + sep},
		{name: "current-dir-suffix", dir: dir + sep + "."},
		{name: "slash-current-dir-suffix", dir: filepath.ToSlash(dir) + "/."},
		{name: "duplicate-separators", dir: filepath.Dir(dir) + sep + sep + filepath.Base(dir) + sep + sep},
		{name: "slash-converted", dir: filepath.ToSlash(dir)},
		{name: "slash-trailing", dir: filepath.ToSlash(dir) + "/"},
	}

	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			uses, err := scanSymbolsWithTimeout(t, tc.dir, pkgs)
			if err != nil {
				t.Fatalf("ScanSymbols(%q): %v", tc.dir, err)
			}
			if _, ok := findUse(uses, "axios", "axios.first"); !ok {
				t.Fatalf("axios.first missing for %q: %+v", tc.dir, uses)
			}
			if _, ok := findUse(uses, "axios", "axios.ignored"); ok {
				t.Fatalf("gitignored state source scanned for %q: %+v", tc.dir, uses)
			}
		})
	}
}

func TestScanSymbolsEmptyDirDoesNotScanCWD(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "cwd.ts"), []byte("import axios from 'axios'; axios.fromCWD();\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	pkgs := []scanner.ResolvedPackage{{PURL: domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.0.0"}}}
	for _, emptyDir := range []string{"", "   ", "\t"} {
		uses, err := scanSymbolsWithTimeout(t, emptyDir, pkgs)
		if err != nil {
			t.Fatalf("ScanSymbols(%q): %v", emptyDir, err)
		}
		if len(uses) != 0 {
			t.Fatalf("ScanSymbols(%q) scanned current directory: %+v", emptyDir, uses)
		}
		directUses, err := scanSymbols(context.Background(), emptyDir, pkgs, defaultSymbolBudget)
		if err != nil {
			t.Fatalf("scanSymbols(%q): %v", emptyDir, err)
		}
		if len(directUses) != 0 {
			t.Fatalf("scanSymbols(%q) scanned current directory: %+v", emptyDir, directUses)
		}
	}
}

func TestGitParentDirsStopsAtBoundaries(t *testing.T) {
	runWithTimeout := func(t *testing.T, fn func()) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			fn()
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("git parent traversal did not terminate within 2s")
		}
	}

	t.Run("escaping-relative-path", func(t *testing.T) {
		dir := filepath.Clean(t.TempDir())
		var files, dirs map[string]bool
		runWithTimeout(t, func() {
			files, dirs = parseGitSourceSet(context.Background(), dir, []byte("../../outside/leak.ts\x00src/ok.ts\x00"))
		})
		if !files[filepath.Join(dir, "src", "ok.ts")] {
			t.Fatalf("missing src/ok.ts in %v", files)
		}
		if !dirs[filepath.Join(dir, "src")] {
			t.Fatalf("missing src parent dir in %v", dirs)
		}
	})

	t.Run("relative-dir-escaping-path", func(t *testing.T) {
		var files, dirs map[string]bool
		runWithTimeout(t, func() {
			files, dirs = parseGitSourceSet(context.Background(), "foo/bar", []byte("../../../outside/leak.ts\x00sub/ok.ts\x00"))
		})
		if !files[filepath.Join("foo", "bar", "sub", "ok.ts")] {
			t.Fatalf("missing sub/ok.ts in %v", files)
		}
		if !dirs[filepath.Join("foo", "bar", "sub")] {
			t.Fatalf("missing sub parent dir in %v", dirs)
		}
	})

	t.Run("root-boundaries", func(t *testing.T) {
		cases := []struct {
			name string
			dir  string
			p    string
		}{
			{name: "unix-root", dir: "/tmp/repo", p: "/other/sub/file.ts"},
			{name: "windows-drive-root", dir: `C:\repo`, p: `D:\other\sub\file.ts`},
			{name: "windows-unc-root", dir: `\\server\share\repo`, p: `\\server\other\sub\file.ts`},
			{name: "at-unix-root", dir: "/", p: "/src/file.ts"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				dirs := map[string]bool{}
				runWithTimeout(t, func() {
					addGitParentDirs(context.Background(), dirs, tc.dir, tc.p)
				})
			})
		}
	})

	t.Run("cancelled-context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var files, dirs map[string]bool
		runWithTimeout(t, func() {
			files, dirs = parseGitSourceSet(ctx, t.TempDir(), []byte("src/ok.ts\x00"))
		})
		if files != nil || dirs != nil {
			t.Fatalf("cancelled context returned maps: files=%v dirs=%v", files, dirs)
		}
	})
}
