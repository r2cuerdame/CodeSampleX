package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

const sdkFailureEnv = "CSX_TEST_SDK_LOCATION_NOT_FOUND"

// helperSDKLocationNotFound is a real child that fails the way an Android
// Gradle build does on a machine with no SDK configured: it works for a while,
// then says so on stderr and exits 1. The delay comes from the environment.
func TestHelperSDKLocationNotFound(t *testing.T) {
	delay := os.Getenv(sdkFailureEnv)
	if delay == "" {
		t.Skip("helper process; run by the bounded-observation tests")
	}
	d, err := time.ParseDuration(delay)
	if err != nil {
		os.Exit(2)
	}
	time.Sleep(d)
	fmt.Fprintln(os.Stderr, "FAILURE: Build failed with an exception.")
	fmt.Fprintln(os.Stderr, "* What went wrong:")
	fmt.Fprintln(os.Stderr, "SDK location not found. Define a valid SDK location with an ANDROID_HOME environment "+
		"variable or by setting the sdk.dir path in your project's local properties file at "+
		"'C:\\Users\\dev\\acme-mobile\\local.properties'.")
	os.Exit(1)
}

func sdkFailureArgv(t *testing.T, delay time.Duration) []string {
	t.Helper()
	t.Setenv(sdkFailureEnv, delay.String())
	return []string{os.Args[0], "-test.run=^TestHelperSDKLocationNotFound$"}
}

// stalled returns a channel that stays open until the test ends. Fixtures
// that block on it ignore their context on purpose: that is the work this
// file exists to survive.
func stalled(t *testing.T) <-chan struct{} {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return release
}

func localOnly() *config.Config {
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	return cfg
}

// A command that failed after seven seconds came back never.
//
// The child exited 1 with "SDK location not found" on stderr — everything the
// agent needed — and the call then waited on the work that follows a command:
// the project scan with its registry probes, the record of the run, the
// lookup of the failure. None of it had a deadline that held against code
// that does not look at its context, so the agent's provider timed the call
// out and the failure was never read.
func TestAFailureAfterSevenSecondsReturnsWhileObservationAndLookupStall(t *testing.T) {
	argv := sdkFailureArgv(t, 7*time.Second)
	release := stalled(t)
	var scans, lookups atomic.Int32
	var asked atomic.Value

	observer := newCommandObserver()
	observer.scan = func(context.Context, string, *registry.Checker) (*scanner.ScanResult, error) {
		scans.Add(1)
		<-release
		return nil, nil
	}
	s := &Server{Deps: &Deps{
		RunObserved: func(ctx context.Context, argv []string, cwd string) (int, string, string, []string, evidence.CommandOutput, error) {
			return observer.run(ctx, nil, nil, localOnly(), nil, nil, argv, cwd)
		},
		Search: func(_ context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			lookups.Add(1)
			asked.Store(req.Query)
			<-release
			return domain.SearchResponse{Miss: true}, ""
		},
	}}

	raw, err := json.Marshal(runArgs{Command: argv, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	returned := make(chan *toolResult, 1)
	go func() { returned <- s.toolRunObserved(context.Background(), raw) }()
	var out *toolResult
	select {
	case out = <-returned:
	case <-time.After(60 * time.Second):
		t.Fatal("run_observed_command never returned: the stalled observation or lookup held the command's result")
	}
	elapsed := time.Since(started)

	if elapsed < 7*time.Second {
		t.Fatalf("returned after %s; the child was supposed to run for 7s", elapsed)
	}
	// The command, then at most one observation wait and one lookup budget.
	if limit := 7*time.Second + observationWait + failureLookupBudget + 8*time.Second; elapsed > limit {
		t.Errorf("returned after %s, beyond the %s the bounds allow", elapsed, limit)
	}
	if scans.Load() != 1 || lookups.Load() != 1 {
		t.Fatalf("the stalled steps were not reached: scans=%d lookups=%d", scans.Load(), lookups.Load())
	}

	if out.IsError {
		t.Fatalf("a failed command became a tool error:\n%s", resultText(out))
	}
	m := structured(t, out)
	if m["exitCode"] != float64(1) {
		t.Errorf("exitCode = %v, want the child's own 1", m["exitCode"])
	}
	if stderr, _ := m["stderr"].(string); !strings.Contains(stderr, "SDK location not found") {
		t.Errorf("the child's stderr was lost: %q", stderr)
	}
	text := resultText(out)
	failure := strings.Index(text, "SDK location not found")
	supporting := strings.Index(text, "CODESAMPLEX SUPPORTING INFORMATION")
	if !strings.HasPrefix(text, "LOCAL COMMAND FAILURE") || failure < 0 || supporting < failure {
		t.Errorf("the local failure is not first in the text:\n%s", text)
	}

	gaps, _ := m["observationIncomplete"].([]any)
	if len(gaps) != 2 || gaps[0] != gapProjectScan || gaps[1] != gapEvidenceRecord {
		t.Errorf("observationIncomplete = %v, want [%s %s]", m["observationIncomplete"], gapProjectScan, gapEvidenceRecord)
	}
	if !strings.Contains(text, "Observation INCOMPLETE") {
		t.Errorf("the text does not say the observation is incomplete:\n%s", text)
	}
	recommendation, _ := m["recommendation"].(map[string]any)
	if recommendation["status"] != "UNAVAILABLE" {
		t.Errorf("recommendation = %v, want UNAVAILABLE", recommendation)
	}
	if reason, _ := recommendation["reason"].(string); !strings.Contains(reason, "did not answer within") {
		t.Errorf("recommendation reason = %q, want the budget it ran out of", reason)
	}

	// What may leave the machine is still only sanitizer output.
	if query, _ := asked.Load().(string); strings.Contains(query, "acme-mobile") {
		t.Errorf("the lookup query carries the project path: %q", query)
	}
	lines, _ := m["sanitizedErrors"].([]any)
	for _, line := range lines {
		if entry, _ := line.(string); strings.Contains(entry, "acme-mobile") {
			t.Errorf("a sanitized line carries the project path: %q", line)
		}
	}
}

// The scan finished; writing the run down did not. The caller still gets the
// classified result, told that the record is behind it.
func TestAStalledRecordReturnsTheClassifiedResult(t *testing.T) {
	argv := sdkFailureArgv(t, 0)
	release := stalled(t)
	ident, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var recordedExit atomic.Int32
	recordedExit.Store(-99)
	observer := newCommandObserver()
	observer.wait = 500 * time.Millisecond
	observer.scan = func(context.Context, string, *registry.Checker) (*scanner.ScanResult, error) {
		return &scanner.ScanResult{}, nil
	}
	observer.record = func(_ context.Context, _ *evidence.Recorder, _ string, _ *scanner.ScanResult,
		_ scanner.CommandProfile, _ []string, exitCode int, _ evidence.CommandOutput) error {
		recordedExit.Store(int32(exitCode))
		<-release
		return nil
	}

	exitCode, _, result, sanitized, output, err := observer.run(t.Context(), nil, ident, localOnly(), nil, nil, argv, t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if exitCode != 1 || result != "FAIL" || !strings.Contains(output.Stderr, "SDK location not found") {
		t.Fatalf("exit/result/stderr = %d/%s/%q", exitCode, result, output.Stderr)
	}
	if len(sanitized) == 0 {
		t.Error("the failure was not diagnosed")
	}
	if !slices.Equal(output.ObservationGaps, []string{gapEvidenceRecord}) {
		t.Errorf("ObservationGaps = %v, want [%s]", output.ObservationGaps, gapEvidenceRecord)
	}
	if got := recordedExit.Load(); got != 1 {
		t.Errorf("the record was handed exit code %d, want 1", got)
	}
}

// Work that ignores cancellation keeps its slot until it returns. Once every
// slot is held, the next command is returned unobserved, and says so, instead
// of waiting behind it; when the stalled work returns, observing resumes.
func TestObservationsInFlightAreBounded(t *testing.T) {
	argv := sdkFailureArgv(t, 0)
	release := make(chan struct{})
	var once atomic.Bool
	t.Cleanup(func() {
		if once.CompareAndSwap(false, true) {
			close(release)
		}
	})
	var scans atomic.Int32
	observer := newCommandObserver()
	observer.wait = 300 * time.Millisecond
	observer.slots = make(chan struct{}, 1)
	observer.scan = func(context.Context, string, *registry.Checker) (*scanner.ScanResult, error) {
		scans.Add(1)
		<-release
		return &scanner.ScanResult{}, nil
	}

	dir := t.TempDir()
	_, _, _, _, first, err := observer.run(t.Context(), nil, nil, localOnly(), nil, nil, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(first.ObservationGaps, []string{gapProjectScan, gapEvidenceRecord}) {
		t.Errorf("first ObservationGaps = %v", first.ObservationGaps)
	}
	code, _, _, _, second, err := observer.run(t.Context(), nil, nil, localOnly(), nil, nil, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(second.Stderr, "SDK location not found") {
		t.Errorf("an unobserved command lost its result: exit=%d stderr=%q", code, second.Stderr)
	}
	if !slices.Equal(second.ObservationGaps, []string{gapObservationCapacity}) {
		t.Errorf("second ObservationGaps = %v, want [%s]", second.ObservationGaps, gapObservationCapacity)
	}
	if got := scans.Load(); got != 1 {
		t.Errorf("%d scans started with one slot held; want 1", got)
	}

	once.Store(true)
	close(release)
	observer.wait = 10 * time.Second
	_, _, _, _, third, err := observer.run(t.Context(), nil, nil, localOnly(), nil, nil, argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.ObservationGaps) != 0 {
		t.Errorf("the released slot was never given back: ObservationGaps = %v", third.ObservationGaps)
	}
}

// Lookups get the same treatment: a bounded wait whether or not Search reads
// its context, and a bounded number still running after their caller left.
func TestFailureLookupsInFlightAreBounded(t *testing.T) {
	release := stalled(t)
	var lookups atomic.Int32
	s := &Server{lookupBudget: 200 * time.Millisecond, Deps: &Deps{
		RunObserved: func(context.Context, []string, string) (int, string, string, []string, evidence.CommandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"SDK location not found"},
				evidence.CommandOutput{Stderr: "SDK location not found"}, nil
		},
		Search: func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			lookups.Add(1)
			<-release
			return domain.SearchResponse{Miss: true}, ""
		},
	}}

	reasons := make([]string, 0, failureLookupConcurrency+1)
	for range failureLookupConcurrency + 1 {
		started := time.Now()
		m := structured(t, s.toolRunObserved(t.Context(), runArgsJSON(t, "gradle", "build")))
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("a stalled lookup held the result for %s", elapsed)
		}
		if m["exitCode"] != float64(1) {
			t.Errorf("exitCode = %v, want 1", m["exitCode"])
		}
		recommendation, _ := m["recommendation"].(map[string]any)
		if recommendation["status"] != "UNAVAILABLE" {
			t.Errorf("recommendation = %v, want UNAVAILABLE", recommendation)
		}
		reason, _ := recommendation["reason"].(string)
		reasons = append(reasons, reason)
	}
	if got := lookups.Load(); got != failureLookupConcurrency {
		t.Errorf("%d lookups started; want at most %d in flight", got, failureLookupConcurrency)
	}
	if last := reasons[len(reasons)-1]; !strings.Contains(last, "earlier lookups are still running") {
		t.Errorf("the lookup refused for capacity said %q", last)
	}
}

// A lookup that panics is a lookup that failed, not a tool call that lost the
// command's result.
func TestAPanickingLookupLeavesTheResult(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, evidence.CommandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"SDK location not found"},
				evidence.CommandOutput{Stderr: "SDK location not found"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			panic("search exploded")
		}
	})
	out := s.toolRunObserved(t.Context(), runArgsJSON(t, "gradle", "build"))
	if out.IsError {
		t.Fatalf("the panic became a tool error:\n%s", resultText(out))
	}
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	if recommendation["status"] != "UNAVAILABLE" || recommendation["reason"] != "the lookup failed" {
		t.Errorf("recommendation = %v", recommendation)
	}
}

// Bounding what happens after the command must not bound the command, or
// stop its own deadline from ending it.
func TestTheCommandTimeoutStillEndsTheCommand(t *testing.T) {
	argv := sdkFailureArgv(t, time.Minute)
	release := stalled(t)
	observer := newCommandObserver()
	observer.wait = 300 * time.Millisecond
	observer.scan = func(context.Context, string, *registry.Checker) (*scanner.ScanResult, error) {
		<-release
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, _, _, _, output, err := observer.run(ctx, nil, nil, localOnly(), nil, nil, argv, t.TempDir())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("a one-second deadline returned after %s", elapsed)
	}
	if output.Termination.Kind != domain.TerminationTimeout {
		t.Errorf("termination = %q, want %q", output.Termination.Kind, domain.TerminationTimeout)
	}
	if strings.Contains(output.Stderr, "SDK location not found") {
		t.Error("the child outlived its deadline")
	}
}
