package sandbox

import (
	"strconv"
	"strings"
	"testing"
)

// The golang resolve stage has to leave a warm build cache behind.
//
// Measured on production 2026-09-01: 39 golang contracts killed at exactly
// 300000ms, every one terminationKind=timeout rather than the 512MB memory
// limit they looked like. Beside them golang holds 1,869 passes, so this is
// the tail of a working lane. An earlier version of this note said golang had
// never passed at all; that was read off a query with a LIMIT on it and was
// wrong.
//
// Go is the only ecosystem whose contract stage compiles anything; every
// other one runs a file that is already there. `go mod download` fills the
// MODULE cache, and GOCACHE — configured for this stage and never written —
// meant each contract compiled the whole dependency graph from source inside
// the 300-second budget.
func TestTheGolangResolveStageWarmsTheBuildCache(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")

	for _, want := range []string{"go mod download", "go build ./..."} {
		if !strings.Contains(script, want) {
			t.Errorf("the golang resolve stage never runs %q:\n%s", want, script)
		}
	}
	// Not vet with it. Measured wall-clock on the same sample: download alone
	// leaves 35.0s of contract; build makes it 18.6s of resolve plus 7.9s of
	// contract; build and vet together make it 22.6s plus 6.4s. Each stage is
	// timed separately, so a budget sees the LARGER of the two, and vet spends
	// four seconds to save one and a half — it raises the number that matters.
	if strings.Contains(script, "go vet") {
		t.Errorf("vet is back; it raises the number the stage budget looks at:\n%s", script)
	}
}

// Warming is an optimisation, and an optimisation may never be why a stage
// fails.
//
// It was. Measured on production after the warm step shipped: twelve golang
// resolves died at exactly 300000ms, having moved the contract's timeout into
// resolve rather than removing it. Bounded well inside the stage budget, the
// build stops when it runs out and the contract compiles what is left — the
// behaviour from before any of this existed.
func TestTheWarmStepCannotConsumeTheStageBudget(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")
	if !strings.Contains(script, "timeout \"$W\" go build") {
		t.Errorf("the warm build is unbounded:\n%s", script)
	}
	bound, err := strconv.Atoi(goResolveDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if float64(bound) >= stageTimeout.Seconds() {
		t.Errorf("the warm bound is %ds against a %.0fs stage budget; it can still be the cause of death",
			bound, stageTimeout.Seconds())
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
	for _, warm := range []string{"go build ./..."} {
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

// The warm build's bound has to be what is LEFT of the stage, not a constant.
//
// Measured on production 2026-09-01, after the fixed 200-second bound
// shipped: golang contract timeouts fell from 19 of 29 receipts to 4 of 65,
// and 31 receipts arrived with the RESOLVE stage killed at the 300-second
// budget instead. `go mod download` and `go list -m -json all` run first and
// neither is bounded, so a slow download plus a full-length warm build
// exceeds the stage on its own. A constant can only ever be wrong in one of
// the two directions; the elapsed time is the only thing that knows.
func TestTheWarmBoundIsWhatIsLeftOfTheStage(t *testing.T) {
	cmd, err := resolveCommand("golang", "")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Join(cmd, " ")

	// It reads the clock before the unbounded part and again after it.
	if strings.Count(script, "date +%s") < 2 {
		t.Errorf("the warm bound is not computed from elapsed time:\n%s", script)
	}
	if strings.Contains(script, "timeout "+goResolveDeadline+" go build") {
		t.Errorf("the warm build still gets a fixed bound, ignoring what download spent:\n%s", script)
	}
	// The clock starts before the part that is not bounded, or the deadline
	// measures the wrong interval.
	if strings.Index(script, "date +%s") > strings.Index(script, "go mod download") {
		t.Errorf("the stage clock starts after the download it is meant to account for:\n%s", script)
	}
	deadline, err := strconv.Atoi(goResolveDeadline)
	if err != nil {
		t.Fatal(err)
	}
	if float64(deadline) >= stageTimeout.Seconds() {
		t.Errorf("the resolve deadline is %ds against a %.0fs stage budget; it leaves no room for the container",
			deadline, stageTimeout.Seconds())
	}
}
