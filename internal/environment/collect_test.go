package environment

import (
	"context"
	"runtime"
	"testing"
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

func TestProbeUnknownTool(t *testing.T) {
	if v := Probe(context.Background(), "definitely-not-a-tool"); v != "" {
		t.Fatalf("unknown tool must return empty, got %q", v)
	}
}
