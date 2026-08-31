package sandbox

import (
	"strings"
	"testing"
)

// The golang resolve stage has to leave a warm build cache behind.
//
// Measured on production 2026-09-01: npm has 3,875 contract passes, pypi 507,
// gem 301, hex 89, maven 62 — and golang has zero, ever, against 39 contracts
// killed at exactly 300000ms. Go is the only ecosystem whose contract stage
// compiles anything; every other one runs a file that is already there.
//
// `go mod download` fills the MODULE cache. GOCACHE, the build cache, was
// configured for this stage and never written, so each contract compiled the
// whole dependency graph from source inside the 300-second budget. Measured
// on a small otel sample: 38.4 CPU-seconds with a cold cache, 4.3 with a warm
// one. On a node losing 79% of its cycles to steal, the first is past the
// budget and the second is not.
func TestTheGolangResolveStageWarmsTheBuildCache(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")

	for _, want := range []string{"go mod download", "go build ./...", "go vet ./..."} {
		if !strings.Contains(script, want) {
			t.Errorf("the golang resolve stage never runs %q:\n%s", want, script)
		}
	}
	// vet as well as build: vet type-checks _test.go, so it compiles the
	// test-only dependencies that `go build ./...` alone leaves out. That
	// difference was 5.3 CPU-seconds against 4.3 in the contract stage.
	if strings.Index(script, "go build ./...") > strings.Index(script, "go vet ./...") {
		t.Error("vet runs before build; build is what fills the cache vet then extends")
	}
}

// It must not run the sample's own code.
//
// This stage is the one with the network. The stage that executes the sample
// runs with --network=none, and that split is the reason a sample's code
// cannot phone home. Warming the cache with `go test` would compile AND run
// the test binary here, which hands sample code a network connection.
func TestTheGolangResolveStageNeverExecutesTheSample(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")
	if strings.Contains(script, "go test") {
		t.Errorf("the resolve stage runs the sample's tests with the network on:\n%s", script)
	}
	if strings.Contains(script, "go run") {
		t.Errorf("the resolve stage runs sample code with the network on:\n%s", script)
	}
}

// A sample that does not compile must still fail at its contract.
//
// Resolve reports whether the dependencies could be fetched. If a compile
// error failed this stage, the receipt would say the dependencies were
// unavailable — a false statement about the network — and the sample's real
// defect would never be attributed to the sample.
func TestACompileFailureDoesNotFailTheGolangResolveStage(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")
	if !strings.Contains(script, "set -e") {
		t.Fatal("the stage no longer stops on a genuine resolve failure")
	}
	for _, warm := range []string{"go build ./...", "go vet ./..."} {
		at := strings.Index(script, warm)
		if at < 0 {
			t.Fatalf("%q is missing", warm)
		}
		rest := script[at:]
		end := strings.Index(rest, ";")
		if end < 0 {
			end = len(rest)
		}
		if !strings.Contains(rest[:end], "|| true") {
			t.Errorf("%q can fail the resolve stage: %q", warm, rest[:end])
		}
	}
}

// Warming may not rewrite what the receipt signs.
//
// `go build` under -mod=mod will happily add a missing requirement to go.mod,
// and go-modules.json — the selected build list this stage persists for the
// receipt — is written before the build runs. If the build could change the
// module graph, the receipt would name a graph the contract did not use.
func TestTheGolangStagesForbidModuleEdits(t *testing.T) {
	env := strings.Join(stageEnv("golang", ""), " ")
	if !strings.Contains(env, "GOFLAGS=-mod=readonly") {
		t.Fatalf("the golang stages no longer forbid module edits: %s", env)
	}
	// And the build list is captured before anything can build.
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")
	if strings.Index(script, "go-modules.json") > strings.Index(script, "go build ./...") {
		t.Error("the build runs before the selected build list is recorded")
	}
}
