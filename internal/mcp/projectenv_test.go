package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

// A search has to be able to learn what project it is standing in.
//
// Deps.MachineEnv was declared and never assigned anywhere in production, so
// machineEnv() fell through to environment.Collect(ctx, nil) -- with NIL
// hints. Every adapter's EnvironmentHints, the whole mechanism by which a
// project describes itself, reached the scan path and nothing else.
//
// For registry ecosystems that was survivable: the agent names its own
// runtime and package manager. For a project whose ONLY public coordinate
// comes from an adapter -- an Unreal project, whose engine version is the one
// thing it can be asked about -- it meant the coordinate existed and no
// search could ever carry it. A subject nothing consumes is the same as no
// subject, which is the failure this repository met three times on
// 2026-09-01 alone.
func TestProjectHintsDescribeAnUnrealProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "MyGame.uproject"),
		[]byte(`{"EngineAssociation":"5.5"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	hints := projectEnvironmentHints(dir)
	if got := hints["frameworks"]; got != "unreal@5.5" {
		t.Errorf("frameworks hint = %q, want %q", got, "unreal@5.5")
	}
}

// A directory that is no project at all yields no hints, and must not make
// the collector invent any.
func TestProjectHintsAreEmptyForAnUnrecognisedDirectory(t *testing.T) {
	if hints := projectEnvironmentHints(t.TempDir()); len(hints) != 0 {
		t.Errorf("an empty directory produced hints: %v", hints)
	}
}

// An unreadable or missing directory is not an error worth failing a search
// over; it is simply a search with nothing extra known about the project.
func TestProjectHintsSurviveAMissingDirectory(t *testing.T) {
	if hints := projectEnvironmentHints(filepath.Join(t.TempDir(), "nope")); len(hints) != 0 {
		t.Errorf("a missing directory produced hints: %v", hints)
	}
}

// And it has to be wired, which is the half that was missing.
//
// The field existed, machineEnv() read it, and nothing ever set it. An
// assertion on the constructor is the only thing that tells the difference
// between a capability and a capability nobody reaches.
func TestTheServerIsGivenAMachineEnvironment(t *testing.T) {
	d, closeDeps, err := NewDeps(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer closeDeps() //nolint:errcheck
	if d.MachineEnv == nil {
		t.Fatal("Deps.MachineEnv is nil, so every search collects an environment " +
			"with no project hints at all")
	}
	env := d.MachineEnv(t.Context())
	if env.OS == "" {
		t.Errorf("the machine environment names no OS: %+v", env)
	}
}
