package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

type observedBlockedTransport struct {
	entered chan struct{}
	release chan struct{}
}

func (r *observedBlockedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-r.release // Deliberately ignores cancellation, like a stuck local/proxy call.
	return nil, errors.New("offline registry fixture")
}
func TestHelperSevenSecondAndroidFailure(t *testing.T) {
	if os.Getenv("CSX_TEST_ANDROID_FAILURE") != "1" {
		return
	}
	time.Sleep(7 * time.Second)
	fmt.Fprintln(os.Stderr, "SDK location not found. Define ANDROID_HOME or sdk.dir in local.properties")
	os.Exit(1)
}
func TestFailedChildDoesNotWaitOnUnboundedRegistryScan(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"package.json":      `{"dependencies":{"axios":"1.12.0"}}`,
		"package-lock.json": `{"name":"fixture","lockfileVersion":3,"packages":{"":{"dependencies":{"axios":"1.12.0"}},"node_modules/axios":{"version":"1.12.0"}}}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	transport := &observedBlockedTransport{entered: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(transport.release)
	t.Setenv("CSX_TEST_ANDROID_FAILURE", "1")
	started := time.Now()
	code, _, result, sanitized, output, err := runObserved(context.Background(), db, nil, cfg, &http.Client{Transport: transport}, nil, []string{os.Args[0], "-test.run=^TestHelperSevenSecondAndroidFailure$"}, dir)
	if err != nil || code != 1 || result != "FAIL" || !strings.Contains(output.Stderr, "SDK location not found") {
		t.Fatalf("child failure lost: %d %s %v %s", code, result, err, output.Stderr)
	}
	if elapsed := time.Since(started); elapsed > 14*time.Second {
		t.Fatalf("completed child was held for %s", elapsed)
	}
	select {
	case <-transport.entered:
	default:
		t.Fatal("registry fixture was not exercised")
	}
	if !strings.Contains(strings.Join(sanitized, " "), "observation incomplete") {
		t.Fatalf("missing partial observation notice: %v", sanitized)
	}
}

type offlineRejectTransport struct {
	calls atomic.Int64
}

func (r *offlineRejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls.Add(1)
	return nil, errors.New("offline test transport: unexpected network request")
}

func TestHelperObservedNodePass(t *testing.T) {
	if os.Getenv("CSX_TEST_OBSERVED_NODE_PASS") != "1" {
		return
	}
	os.Exit(0)
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open test binary %s: %v", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("create shim %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatalf("copy shim %s: %v", dst, err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close shim %s: %v", dst, err)
	}
}

func observedNodeOrTscCommand(t *testing.T) ([]string, domain.Stage) {
	t.Helper()
	if nodePath, err := exec.LookPath("node"); err == nil {
		return []string{nodePath, "-e", ""}, domain.StageProjectProcess
	}
	if runtime.GOOS != "windows" {
		tscPath := filepath.Join(t.TempDir(), "tsc")
		if err := os.WriteFile(tscPath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
			t.Fatalf("write tsc script: %v", err)
		}
		return []string{tscPath, "--noEmit"}, domain.StageProjectTypecheck
	}
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	tscPath := filepath.Join(t.TempDir(), "tsc.exe")
	copyExecutable(t, exe, tscPath)
	return []string{tscPath, "-test.run=^TestHelperObservedNodePass$"}, domain.StageProjectTypecheck
}

func TestRunObservedNonCanonicalCwdCompletesWithoutExhaustingWorkers(t *testing.T) {
	t.Setenv("CSX_HOME", t.TempDir())
	cacheDir := t.TempDir()
	t.Setenv("LOCALAPPDATA", cacheDir)
	t.Setenv("XDG_CACHE_HOME", cacheDir)
	t.Setenv("CSX_TEST_OBSERVED_NODE_PASS", "1")

	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	files := map[string]string{
		".gitignore":        "state/\n",
		"package.json":      `{"name":"observed-fixture","packageManager":"npm@10.9.0","dependencies":{"axios":"1.12.0"}}`,
		"package-lock.json": `{"name":"observed-fixture","lockfileVersion":3,"packages":{"":{"dependencies":{"axios":"1.12.0"}},"node_modules/axios":{"version":"1.12.0","resolved":"https://registry.npmjs.org/axios/-/axios-1.12.0.tgz"}}}`,
		"src/index.ts":      "import axios from 'axios';\nexport async function ping() { return axios.post('/ping', {}); }\n",
		"state/ignored.ts":  "import axios from 'axios';\naxios.ignored();\n",
	}
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	argv, wantStage := observedNodeOrTscCommand(t)

	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	axiosPURL := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := db.SetPublicness(ctx, axiosPURL, scanner.PublicnessPublic); err != nil {
		t.Fatalf("SetPublicness: %v", err)
	}

	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	transport := &offlineRejectTransport{}
	httpClient := &http.Client{Transport: transport}

	sep := string(filepath.Separator)
	cwds := []string{
		dir + sep,
		filepath.ToSlash(dir),
		filepath.ToSlash(dir) + "/",
		dir + sep + ".",
		filepath.ToSlash(dir) + "/.",
		filepath.Dir(dir) + sep + sep + filepath.Base(dir) + sep,
	}
	if len(cwds) <= cap(observedWorkers) {
		t.Fatalf("test needs more than %d calls to prove worker pool does not exhaust", cap(observedWorkers))
	}

	for i, cwd := range cwds {
		// Force a symbol cache miss on every iteration so gitSourceSet runs each time.
		if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("state/\n# iter "+strconv.Itoa(i)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		code, stage, result, sanitized, _, err := runObserved(ctx, db, ident, cfg, httpClient, nil, argv, cwd)
		elapsed := time.Since(started)
		if err != nil || code != 0 || result != string(domain.ResultPass) {
			t.Fatalf("call %d (%q) failed after %s: code=%d stage=%q result=%q sanitized=%v err=%v",
				i, cwd, elapsed, code, stage, result, sanitized, err)
		}
		if stage != string(wantStage) {
			t.Fatalf("call %d (%q) stage = %q, want %s (sanitized=%v)",
				i, cwd, stage, wantStage, sanitized)
		}
		joined := strings.Join(sanitized, " ")
		if strings.Contains(joined, "observation incomplete") ||
			strings.Contains(joined, "post-command deadline exceeded") ||
			strings.Contains(joined, "observer capacity unavailable") {
			t.Fatalf("call %d (%q) reported incomplete observation after %s: %v", i, cwd, elapsed, sanitized)
		}
	}

	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("offline cached test made %d network call(s)", got)
	}

	rows, err := db.PendingObservations(ctx, 100)
	if err != nil {
		t.Fatalf("PendingObservations: %v", err)
	}
	var sawPkg, sawSym bool
	for _, r := range rows {
		if r.Symbol == "axios.ignored" {
			t.Fatalf("gitignored symbol recorded in observations: %+v", r)
		}
		if r.PURL != axiosPURL.String() || r.Stage != wantStage || r.Result != domain.ResultPass {
			continue
		}
		if r.Symbol == "" && r.Count == len(cwds) {
			sawPkg = true
		}
		if r.Symbol == "axios.post" && r.Count == len(cwds) {
			sawSym = true
		}
	}
	if !sawPkg || !sawSym {
		t.Fatalf("missing expected observations (sawPkg=%v sawSym=%v): %+v", sawPkg, sawSym, rows)
	}

	symRows, err := db.SymbolUsages(ctx, axiosPURL)
	if err != nil {
		t.Fatalf("SymbolUsages: %v", err)
	}
	var sawSymbolUsage bool
	for _, s := range symRows {
		if s.Symbol == "axios.ignored" {
			t.Fatalf("gitignored symbol recorded in symbol_usages: %+v", s)
		}
		if s.Symbol == "axios.post" {
			sawSymbolUsage = true
		}
	}
	if !sawSymbolUsage {
		t.Fatalf("axios.post missing from symbol_usages: %+v", symRows)
	}
}
