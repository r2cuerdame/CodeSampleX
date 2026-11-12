package environment

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestCollectBasics(t *testing.T) {
	fp := Collect(context.Background(), map[string]string{
		"ecosystem": "npm", "runtime": "node", "runtimeVersion": "22.18",
		"packageManager": "pnpm", "packageManagerVersion": "10",
		"moduleSystem": "esm", "frameworks": "next@16",
	})
	if fp.SchemaVersion != 1 || fp.OS != runtime.GOOS || fp.Arch == "" {
		t.Fatalf("bad base fields: %+v", fp)
	}
	if fp.Ecosystem != "npm" || fp.RuntimeVersion != "22.18" || fp.ModuleSystem != "esm" {
		t.Fatalf("hints not applied: %+v", fp)
	}
	if len(fp.Frameworks) != 1 || fp.Frameworks[0] != "next@16" {
		t.Fatalf("frameworks not applied: %+v", fp.Frameworks)
	}
	if runtime.GOOS == "windows" && fp.OSVersionBucket == "" {
		t.Error("windows os bucket empty")
	}
}

func TestProbeGo(t *testing.T) {
	// The build machine has go by definition.
	v := Probe(context.Background(), "go")
	if v == "" {
		t.Fatal("probing go returned empty version")
	}
}

// TestCollectRecordsIsolation pins the dimensions that separate a
// container build from a host build. Without them an alpine (musl)
// container and an ubuntu (glibc) host both record as plain "linux",
// which is exactly the split that breaks packages with native modules.
func TestCollectRecordsIsolation(t *testing.T) {
	fp := Collect(t.Context(), map[string]string{
		"ecosystem": "npm", "runtime": "node", "runtimeVersion": "22.18",
		"virtualization": "container", "containerRuntime": "docker", "libc": "musl",
	})
	if fp.Virtualization != "container" || fp.ContainerRuntime != "docker" || fp.Libc != "musl" {
		t.Fatalf("isolation hints not applied: %+v", fp)
	}
	// The dimensions change the identity of the environment: a musl
	// container must never hash the same as the equivalent glibc host.
	host := fp
	host.Virtualization, host.ContainerRuntime, host.Libc = "", "", "glibc"
	if host.Hash() == fp.Hash() {
		t.Error("container/libc dimensions do not affect the environment hash")
	}
}

func TestDetectCI(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	if DetectCI() {
		t.Error("no CI markers set, but DetectCI reported true")
	}
	t.Setenv("CI", "true")
	if !DetectCI() {
		t.Error("CI=true not detected")
	}
	// A runner that explicitly disables the flag is not CI.
	t.Setenv("CI", "false")
	if DetectCI() {
		t.Error("CI=false must not count as CI")
	}
}

// TestProbeOrphanHelper is the probed tool: it hands its stdout to a
// grandchild that outlives it, the shape of npm.cmd -> node on Windows.
func TestProbeOrphanHelper(t *testing.T) {
	pidFile := os.Getenv("CSX_PROBE_ORPHAN")
	if pidFile == "" {
		return
	}
	if os.Getenv("CSX_PROBE_GRANDCHILD") == "1" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestProbeOrphanHelper$")
	child.Env = append(os.Environ(), "CSX_PROBE_GRANDCHILD=1")
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// A probe deadline must bound the whole call, not only the direct child
// (#689 full go test hung ten minutes in an npm probe).
func TestProbeReturnsAtDeadlineWhenGrandchildHoldsOutput(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	t.Setenv("CSX_PROBE_ORPHAN", pidFile)
	probeable["csx-orphan-probe"] = probe(os.Args[0], "-test.run=^TestProbeOrphanHelper$")
	t.Cleanup(func() { delete(probeable, "csx-orphan-probe") })
	t.Cleanup(func() {
		// The grandchild runs this test executable; leaving it alive locks
		// the binary against Go's temporary-directory cleanup.
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(string(raw)); err == nil {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
					_, _ = p.Wait()
				}
			}
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	start := time.Now()
	if v := Probe(ctx, "csx-orphan-probe"); v != "" {
		t.Fatalf("timed-out probe returned version %q", v)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("probe waited %s for an orphaned grandchild's output", elapsed)
	}
	if runtime.GOOS != "windows" {
		return
	}
	// On Windows the survivor would keep the caller's working directory, so a
	// test that probes from a t.Chdir directory could not remove it.
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("helper never published its grandchild: %v", err)
	}
	pid, _ := strconv.Atoi(string(raw))
	p, err := os.FindProcess(pid)
	if err != nil {
		return // already gone
	}
	exited := make(chan struct{})
	go func() { _, _ = p.Wait(); close(exited) }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatalf("grandchild %d outlived the probe deadline", pid)
	}
}

func TestProbeUnknownTool(t *testing.T) {
	if v := Probe(context.Background(), "definitely-not-a-tool"); v != "" {
		t.Fatalf("unknown tool must return empty, got %q", v)
	}
}

func TestPublicCLIToolsHaveFixedLocalProbeCommands(t *testing.T) {
	for _, name := range []string{
		"npm", "pnpm", "maven", "gradle", "gem", "bundler", "gh",
		"git", "curl", "jq", "openssl", "docker", "kubectl", "helm",
		"terraform", "opentofu", "powershell", "cmd",
	} {
		spec, ok := probeable[name]
		if !ok || spec.command == "" {
			t.Errorf("CLI target %q has no fixed probe command", name)
		}
	}
	if probeable["maven"].command != "mvn" || probeable["bundler"].command != "bundle" ||
		probeable["opentofu"].command != "tofu" {
		t.Fatalf("logical CLI names do not map to their real executables: %+v", probeable)
	}
}
