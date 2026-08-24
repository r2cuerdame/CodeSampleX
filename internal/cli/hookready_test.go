package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hookReadyHarness points both homes at throwaway directories: the csx home
// where the verification record lives, and the agent home the registrations
// are read from. Nothing here touches the developer's real ~/.claude or
// ~/.codex.
func hookReadyHarness(t *testing.T) (csxHome, agentHome string) {
	t.Helper()
	csxHome, agentHome = t.TempDir(), t.TempDir()
	t.Setenv("CSX_HOME", csxHome)
	t.Setenv(agentHomeEnv, agentHome)
	return csxHome, agentHome
}

// hookStatusText runs `csx hook status` the way a user would and returns what
// they see.
func hookStatusText(t *testing.T, smoke hookSmokeFunc) string {
	t.Helper()
	var out, errOut bytes.Buffer
	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	if smoke != nil {
		env.smoke = smoke
	}
	if code := hookSwitchMain(context.Background(), []string{"status"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook status exited %d: %s", code, errOut.String())
	}
	return out.String()
}

func wantIn(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("output is missing %q:\n%s", w, got)
		}
	}
}

func wantNotIn(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if strings.Contains(got, w) {
			t.Errorf("output should not contain %q:\n%s", w, got)
		}
	}
}

// The gap this exists to close. One-time init output is not a status model: a
// user leaves setup and cannot afterwards tell whether the lookup is actually
// wired up. Every state the audit named has to be distinguishable, and an
// agent that was never installed must not be reported as a failure.
func TestHookStatusDistinguishesRegisteredFromAbsent(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}

	got := hookStatusText(t, nil)
	wantIn(t, got, "Claude Code", "registered", filepath.Join(agentHome, ".claude", "settings.json"))
	// Codex was never installed on this machine, which is not a problem to
	// report — it is simply not one of this user's agents.
	wantIn(t, got, "Codex", "not detected")
	// Registered is not the same claim as working, and the difference is the
	// whole point.
	wantNotIn(t, got, "verified")
}

// Codex will not run a hook it has not been told to trust, and it exposes no
// way to read that decision back. Saying "installed and active" would be a
// guess presented as a fact.
func TestHookStatusSaysCodexApprovalIsNotVerifiable(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".codex")
	if _, err := installCodex(agentHome); err != nil {
		t.Fatal(err)
	}

	got := hookStatusText(t, nil)
	wantIn(t, got, "Codex", "approval not verifiable", "/hooks")
}

// A registration that could not be created is its own state. On Windows the
// installer refuses to write a Codex command containing a space, because
// Codex will not run one — and an install that silently registered nothing
// must not read as "registered".
func TestHookStatusReportsARegistrationThatCouldNotBeCreated(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".codex")
	// The csx block is there — the MCP server registered fine — but it
	// carries no hook, which is exactly what codexHookCommand refusing looks
	// like on disk.
	writeFile(t, filepath.Join(agentHome, ".codex", "config.toml"),
		tomlBegin+"\n[mcp_servers.csx]\ncommand = \"csx\"\nargs = [\"mcp\"]\n"+tomlEnd+"\n")

	got := hookStatusText(t, nil)
	wantIn(t, got, "Codex", hookStateUnavailable, "space")
	wantIn(t, got, "csx init")
	// "unavailable" and "not registered" need opposite fixes and must not
	// collapse into one word.
	wantNotIn(t, got, "Codex        registered")
}

// An agent whose config carries nothing of ours is a different state again:
// the installer never ran for it, or its block was removed.
func TestHookStatusSeparatesNothingRegisteredFromABrokenRegistration(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	writeFile(t, filepath.Join(agentHome, ".claude", "settings.json"), `{"theme":"dark"}`)

	wantIn(t, hookStatusText(t, nil), "Claude Code", hookStateNotRegistered, "csx init")
}

// The case an update actually produces. The registration still reads like a
// working one and names a binary that is not there any more — which is the
// silent failure, not a loud one.
func TestHookStatusCatchesARegistrationPointingAtAMissingBinary(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(agentHome, ".claude", "settings.json")
	gone := filepath.Join(t.TempDir(), "csx-from-the-last-version")
	writeFile(t, settings, strings.Replace(readFile(t, settings),
		jsonQuoted(t, mcpCommand()), jsonQuoted(t, gone), 1))

	got := hookStatusText(t, nil)
	wantIn(t, got, "Claude Code", hookStateUnavailable, "not on disk")
	wantNotIn(t, got, "verified")
}

func jsonQuoted(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// The install screen scrolls away. What it promised has to stay checkable
// from a command the reader was told about while they could still see it.
func TestInstallPointsAtTheDurableReadinessCommand(t *testing.T) {
	got := hookReadinessFollowUp([]agentInstallResult{
		{Agent: hookAgentClaude, Actions: []string{"installed build-failure lookup → x"}},
	})
	wantIn(t, got, "csx hook status", "csx hook check")

	// Nothing to point at when no agent that takes the lookup was set up.
	for _, results := range [][]agentInstallResult{
		nil,
		{{Agent: "Gemini CLI", Actions: []string{"installed MCP server → x"}}},
		{{Agent: hookAgentCodex, Skipped: true, Reason: "not detected"}},
	} {
		if follow := hookReadinessFollowUp(results); follow != "" {
			t.Errorf("results %v produced a readiness pointer: %q", results, follow)
		}
	}
}

// The one switch, visible where readiness is read. A user who turned the
// lookup off and forgot must not be left reading a registration line that
// looks live.
func TestHookStatusShowsTheOffSwitch(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	if code := hookSwitchMain(context.Background(), []string{"off"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook off exited %d: %s", code, errOut.String())
	}

	got := hookStatusText(t, nil)
	wantIn(t, got, "build-failure lookup: off", "csx hook on")
	// Still registered — off is a csx-side switch, not a de-registration —
	// but nothing may present it as ready.
	wantNotIn(t, got, "verified")
}

// A smoke check is the only thing that can turn "registered" into a claim
// about behaviour, and it has to prove the whole path: a failing build,
// through the exact command the agent will run, into a lookup that answered.
func TestHookCheckRecordsWhatItProved(t *testing.T) {
	csxHome, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.smoke = func(context.Context, hookRegistration) hookSmokeResult {
		return hookSmokeResult{Code: hookTraceNoMatch, Reached: true}
	}
	if code := hookSwitchMain(context.Background(), []string{"check"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook check exited %d:\n%s\n%s", code, out.String(), errOut.String())
	}
	wantIn(t, out.String(), "Claude Code", "verified")

	// And it is durable: a later status reads the record rather than
	// re-running anything.
	wantIn(t, hookStatusText(t, nil), "verified")

	if _, err := os.Stat(filepath.Join(csxHome, hookCheckFileName)); err != nil {
		t.Fatalf("the check proved something and recorded nothing: %v", err)
	}
}

// The acceptance criterion an update has to survive. Trust — Codex's, and our
// own record of a smoke check — is recorded against the hook's EXACT
// definition. When an update rewrites that definition, the old verdict is
// about a hook that no longer exists, and presenting it as current is the
// silent failure this issue is about.
func TestChangingTheHookDefinitionInvalidatesTheOldVerdict(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.smoke = func(context.Context, hookRegistration) hookSmokeResult {
		return hookSmokeResult{Code: hookTraceAnswered, Reached: true}
	}
	if code := hookSwitchMain(context.Background(), []string{"check"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook check exited %d: %s", code, errOut.String())
	}
	wantIn(t, hookStatusText(t, nil), "verified")

	// An update lands and the registered definition is not the one that was
	// proved: same binary, different timeout.
	settings := filepath.Join(agentHome, ".claude", "settings.json")
	raw := readFile(t, settings)
	writeFile(t, settings, strings.Replace(raw, `"timeout": 30`, `"timeout": 45`, 1))
	if readFile(t, settings) == raw {
		t.Fatal("the test could not change the registered definition")
	}

	got := hookStatusText(t, nil)
	wantNotIn(t, got, "verified —")
	wantIn(t, got, "changed since", "csx hook check")
}

// Same rule, stated where it costs the most: Codex records trust against the
// definition too, so a changed definition means Codex is skipping the hook
// again until somebody re-approves it in /hooks.
func TestACodexDefinitionChangeAsksForReapproval(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".codex")
	if _, err := installCodex(agentHome); err != nil {
		t.Fatal(err)
	}

	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	env.smoke = func(context.Context, hookRegistration) hookSmokeResult {
		return hookSmokeResult{Code: hookTraceNoMatch, Reached: true}
	}
	var out, errOut bytes.Buffer
	if code := hookSwitchMain(context.Background(), []string{"check"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook check exited %d: %s", code, errOut.String())
	}

	toml := filepath.Join(agentHome, ".codex", "config.toml")
	raw := readFile(t, toml)
	writeFile(t, toml, strings.Replace(raw, "timeout = 30", "timeout = 45", 1))
	if readFile(t, toml) == raw {
		t.Fatal("the test could not change the registered definition")
	}

	got := hookStatusText(t, nil)
	wantIn(t, got, "changed since", "/hooks")
}

// The fingerprint is over what the agent will actually run. Two definitions
// that differ anywhere the agent can see must not share a verdict.
func TestHookFingerprintCoversTheWholeDefinition(t *testing.T) {
	base := hookDefinition{
		Agent: "Claude Code", Event: failureEvent, Matcher: "Bash",
		Command: "/usr/local/bin/csx", Args: []string{"hook", "agent"}, Timeout: 30,
	}
	seen := map[string]string{base.fingerprint(): "base"}
	for name, d := range map[string]hookDefinition{
		"other binary": {Agent: base.Agent, Event: base.Event, Matcher: base.Matcher, Command: "/opt/csx", Args: base.Args, Timeout: base.Timeout},
		"other args":   {Agent: base.Agent, Event: base.Event, Matcher: base.Matcher, Command: base.Command, Args: []string{"hook", "agent", "--v2"}, Timeout: base.Timeout},
		"other event":  {Agent: base.Agent, Event: "PostToolUse", Matcher: base.Matcher, Command: base.Command, Args: base.Args, Timeout: base.Timeout},
		"other match":  {Agent: base.Agent, Event: base.Event, Matcher: "^Bash$", Command: base.Command, Args: base.Args, Timeout: base.Timeout},
		"other budget": {Agent: base.Agent, Event: base.Event, Matcher: base.Matcher, Command: base.Command, Args: base.Args, Timeout: 45},
	} {
		fp := d.fingerprint()
		if prior, dup := seen[fp]; dup {
			t.Errorf("%q and %q share a fingerprint, so a change between them is invisible", name, prior)
		}
		seen[fp] = name
	}
	// And it is stable: the same definition read twice is the same verdict.
	if base.fingerprint() != base.fingerprint() {
		t.Error("the fingerprint is not stable, so every read invalidates the last check")
	}
}

// The smoke check must not need — or touch — a real project. It builds a
// throwaway one, and what it feeds the hook has to be a failure the hook will
// actually recognise as a build step, or the check proves nothing.
func TestHookSmokeCheckFeedsAThrowawayFailingBuild(t *testing.T) {
	for _, agent := range []string{hookAgentClaude, hookAgentCodex} {
		dir, payload, cleanup, err := hookSmokeProject(agent)
		if err != nil {
			t.Fatalf("%s: %v", agent, err)
		}
		defer cleanup()

		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			t.Errorf("%s: the throwaway project has nothing for the scanner to detect: %v", agent, err)
		}
		var p hookPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("%s: the hook cannot parse its own smoke payload: %v", agent, err)
		}
		if p.CWD != dir {
			t.Errorf("%s: payload cwd = %q, want the throwaway project %q", agent, p.CWD, dir)
		}
		if p.ToolName != "Bash" {
			t.Errorf("%s: tool_name = %q, want Bash", agent, p.ToolName)
		}
		// The command has to classify as a build step, using the same
		// classifier the hook itself uses. Anything else and the hook stays
		// quiet for a reason that has nothing to do with whether it is wired
		// up.
		segs := hookSegments(p.ToolInput.Command)
		if len(segs) == 0 {
			t.Fatalf("%s: nothing to classify in %q", agent, p.ToolInput.Command)
		}
		// Both agents have to arrive at "this failed" through their own path
		// — Claude from `error`, Codex from the rollout file's exit code.
		text, failed := hookFailureText(p)
		if !failed || text == "" {
			t.Errorf("%s: the hook cannot tell the smoke payload failed", agent)
		}
	}
}

// A hook that stayed quiet and a hook that was never reached look identical
// from outside. The trace is what tells them apart, so the codes the smoke
// check reads have to be the codes the hook writes.
func TestHookTraceCodesAreUniqueAndPrinted(t *testing.T) {
	seen := map[string]bool{}
	for _, code := range hookTraceCodes {
		if code == "" {
			t.Error("an empty trace code cannot be read back")
		}
		if seen[code] {
			t.Errorf("duplicate trace code %q", code)
		}
		seen[code] = true
	}

	// The hook prints one when it is told to explain itself.
	debug := &bytes.Buffer{}
	env, _ := hookHarness(t, hookInput(t, "Bash", "git status", "fatal"), func(e *hookEnv) {
		e.debug = debug
		e.inspect = func(context.Context, string, [][]string) hookProject {
			return hookProject{Known: false}
		}
	})
	hookAgentMain(context.Background(), env)
	if got := hookTraceCode(debug.String()); got != hookTraceNotBuildStep {
		t.Errorf("trace code = %q, want %q (from %q)", got, hookTraceNotBuildStep, debug.String())
	}
}

// The verification record is read, not trusted blindly: a file from an older
// csx, or one somebody edited, must degrade to "not verified" rather than
// crash or claim more than it knows.
func TestUnreadableCheckRecordIsNotAClaim(t *testing.T) {
	csxHome, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(csxHome, hookCheckFileName), "{not json")

	got := hookStatusText(t, nil)
	wantIn(t, got, "registered")
	wantNotIn(t, got, "verified —")
}

// Time is part of the claim: "verified" without a when is an assertion that
// never expires.
func TestVerifiedStateNamesWhenItWasProved(t *testing.T) {
	csxHome, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}
	reg := hookRegistrations(agentHome)
	var claude hookRegistration
	for _, r := range reg {
		if r.Agent == hookAgentClaude {
			claude = r
		}
	}
	if claude.Fingerprint == "" {
		t.Fatal("no Claude registration was found to record against")
	}
	when := time.Date(2026, 8, 20, 9, 30, 0, 0, time.UTC)
	if err := saveHookCheck(csxHome, claude.Agent, hookCheckRecord{
		Fingerprint: claude.Fingerprint, CheckedAt: when, Version: "0.1.44", Outcome: hookTraceAnswered,
	}); err != nil {
		t.Fatal(err)
	}

	wantIn(t, hookStatusText(t, nil), "verified", "2026-08-20")
}

// hookSmokeHelperEnv turns this test binary into the executable the smoke
// check will run, so the exec path itself — stdin payload, environment, the
// trace read back off stderr — is exercised rather than mocked.
const (
	hookSmokeHelperEnv  = "CSX_HOOK_SMOKE_HELPER"
	hookSmokeHelperCode = "CSX_HOOK_SMOKE_HELPER_CODE"
)

// TestHookSmokeHelperProcess is not a test. It is the fake hook the case
// below registers, and it runs only in the child process.
func TestHookSmokeHelperProcess(t *testing.T) {
	if os.Getenv(hookSmokeHelperEnv) == "" {
		t.Skip("not the helper process")
	}
	// Everything from here writes the child's own stdout/stderr and exits, so
	// the test framework never prints over the hook's output.
	fail := func(why string) {
		fmt.Fprintln(os.Stderr, "helper: "+why)
		os.Exit(3)
	}

	var p hookPayload
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		fail("the smoke check sent something a hook cannot parse: " + err.Error())
	}
	if os.Getenv("CSX_HOOK_DEBUG") == "" {
		fail("the smoke check did not ask the hook to explain itself, so it could read nothing back")
	}
	if _, err := os.Stat(filepath.Join(p.CWD, "package.json")); err != nil {
		fail("the smoke check did not build a project to fail in: " + err.Error())
	}
	if _, failed := hookFailureText(p); !failed {
		fail("the smoke check sent something that does not read as a failure")
	}

	code := os.Getenv(hookSmokeHelperCode)
	if code == hookTraceAnswered {
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName":     p.HookEventName,
			"additionalContext": "CODESAMPLEX SUPPORTING INFORMATION — SECONDARY",
		}})
		os.Stdout.Write(out)
		os.Exit(0)
	}
	fmt.Fprintf(os.Stderr, "csx hook: [%s] from the helper\n", code)
	os.Exit(0)
}

// The check has to run the command the agent runs, feed it a real failing
// build, and believe only what comes back. Every one of those steps is a
// place the verdict could quietly become a guess.
func TestSmokeCheckReadsTheHookItActuallyRan(t *testing.T) {
	for _, c := range []struct {
		code        string
		wantReached bool
		wantCode    string
	}{
		// The lookup ran and answered: the whole path is proved.
		{hookTraceAnswered, true, hookTraceAnswered},
		// The lookup ran and this network knew nothing. That is an honest
		// answer about the network, not a fault in the wiring — which is the
		// only thing this check is measuring.
		{hookTraceNoMatch, true, hookTraceNoMatch},
		// The lookup ran, answered, and the relevance gate declined to
		// interrupt with what came back. That is the gate working, and it
		// proves the same path as an answer does — reading it as "stopped
		// early" would report a working install as broken.
		{hookTraceUnrelated, true, hookTraceUnrelated},
		{hookTraceLowRelevance, true, hookTraceLowRelevance},
		// Reached the hook, but never reached the lookup. Not proved.
		{hookTraceSearchFailed, false, hookTraceSearchFailed},
		{hookTraceNotBuildStep, false, hookTraceNotBuildStep},
		{hookTraceOff, false, hookTraceOff},
	} {
		t.Setenv(hookSmokeHelperEnv, "1")
		t.Setenv(hookSmokeHelperCode, c.code)
		reg := hookRegistration{
			Agent: hookAgentClaude,
			Exe:   os.Args[0],
			Args:  []string{"-test.run=TestHookSmokeHelperProcess"},
		}
		got := hookSmoke(context.Background(), reg)
		if got.Err != nil {
			t.Fatalf("%s: the check could not run the registered command: %v", c.code, got.Err)
		}
		if got.Code != c.wantCode {
			t.Errorf("%s: code = %q, want %q", c.code, got.Code, c.wantCode)
		}
		if got.Reached != c.wantReached {
			t.Errorf("%s: reached = %v, want %v (%s)", c.code, got.Reached, c.wantReached, got.Detail)
		}
		if strings.TrimSpace(got.Detail) == "" {
			t.Errorf("%s: a verdict with no sentence tells the reader nothing", c.code)
		}
	}
}

// A registered command that cannot run at all is the state an install must
// never present as ready — and it is a real one: the binary was moved,
// renamed, or removed by an update.
func TestSmokeCheckReportsACommandThatCannotRun(t *testing.T) {
	reg := hookRegistration{
		Agent: hookAgentClaude,
		Exe:   filepath.Join(t.TempDir(), "csx-that-is-not-there"),
		Args:  []string{"hook", "agent"},
	}
	got := hookSmoke(context.Background(), reg)
	if got.Reached {
		t.Error("a command that does not exist was reported as reached")
	}
	if got.Err == nil {
		t.Error("nothing said why it could not run")
	}
}

// The Codex path is a different code path, not a different label: no
// failure-only event and no exit code in the payload, so the hook recovers one
// from the rollout file. A check that skipped that would prove the half Codex
// does not use.
func TestSmokeCheckExercisesTheCodexRolloutPath(t *testing.T) {
	t.Setenv(hookSmokeHelperEnv, "1")
	t.Setenv(hookSmokeHelperCode, hookTraceNoMatch)
	reg := hookRegistration{
		Agent: hookAgentCodex,
		Exe:   os.Args[0],
		Args:  []string{"-test.run=TestHookSmokeHelperProcess"},
	}
	got := hookSmoke(context.Background(), reg)
	if got.Err != nil {
		t.Fatalf("the Codex smoke check could not run: %v", got.Err)
	}
	if !got.Reached {
		t.Errorf("the Codex payload did not read as a failing build: %s (%s)", got.Code, got.Detail)
	}
}

// A check that fails today outranks a pass from last week. The pass really
// happened, but it is no longer the current state, and a status that keeps
// reciting it is the same silent lie as an inherited trust assumption.
func TestAFailedRecheckRetiresTheOldPass(t *testing.T) {
	_, agentHome := hookReadyHarness(t)
	plantDir(t, agentHome, ".claude")
	if _, err := installClaude(agentHome); err != nil {
		t.Fatal(err)
	}

	env, err := newHookReadyEnv()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	env.smoke = func(context.Context, hookRegistration) hookSmokeResult {
		return hookSmokeResult{Code: hookTraceAnswered, Reached: true}
	}
	if code := hookSwitchMain(context.Background(), []string{"check"}, &out, &errOut, env); code != 0 {
		t.Fatalf("csx hook check exited %d: %s", code, errOut.String())
	}
	wantIn(t, hookStatusText(t, nil), "verified")

	// The daemon is down, or the binary broke. Same definition, no proof.
	out.Reset()
	env.smoke = func(context.Context, hookRegistration) hookSmokeResult {
		return hookSmokeResult{Code: hookTraceSearchFailed, Detail: "the lookup could not complete"}
	}
	if code := hookSwitchMain(context.Background(), []string{"check"}, &out, &errOut, env); code == 0 {
		t.Error("a check that proved nothing exited 0")
	}
	wantIn(t, out.String(), "NOT PROVED")
	wantNotIn(t, hookStatusText(t, nil), "verified —")
}
