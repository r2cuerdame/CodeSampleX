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

func TestProbeUnknownTool(t *testing.T) {
	if v := Probe(context.Background(), "definitely-not-a-tool"); v != "" {
		t.Fatalf("unknown tool must return empty, got %q", v)
	}
}
