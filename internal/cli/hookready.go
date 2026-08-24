package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// Readiness, after the install screen has scrolled away.
//
// `csx init` says the true thing once: Codex will skip the build-failure
// lookup until somebody runs /hooks and trusts it. But a one-time sentence is
// not a status model. The user leaves setup and cannot afterwards tell whether
// they ever did it; an update rewrites the hook definition and the trust that
// was granted was granted to a definition that no longer exists; a
// registration that could not be created at all looks exactly like one that
// was; and `csx hook off` is invisible from anywhere the registration is read.
//
// So this is the durable answer to "is it actually wired up", and its rule is
// that it may only say what it can show:
//
//   - registered is a fact about a config file, and it is not a claim that
//     anything ever ran;
//   - verified is a claim about behaviour, and only a smoke check that fed a
//     real failing build through the exact registered command may make it;
//   - Codex approval is not readable by anyone but Codex, so it is reported as
//     not verifiable — never as granted, and never as missing;
//   - every verdict is bound to the fingerprint of the exact definition it was
//     made against, so a definition change retires it instead of inheriting it.
const (
	hookAgentClaude = "Claude Code"
	hookAgentCodex  = "Codex"
)

// The states a reader has to be able to tell apart.
const (
	hookStateNotDetected   = "not detected"
	hookStateNotRegistered = "not registered"
	hookStateUnavailable   = "unavailable"
	hookStateRegistered    = "registered"
	hookStateVerified      = "verified"
	hookStateOff           = "off"
)

// hookApprovalNotVerifiable is the exact sentence for the thing we cannot
// know. Codex records trust against a hook's definition and offers no way to
// read that decision back, so this is where the status model stops.
const hookApprovalNotVerifiable = "Codex approval not verifiable"

// hookCodexTrustAction is the one manual step an install cannot perform.
const hookCodexTrustAction = "run /hooks in Codex and trust the csx build-failure lookup"

// hookDefinition is the hook exactly as the agent will run it. It is what the
// fingerprint is taken over, because it is what Codex records trust against
// and what a smoke check actually exercised — anything less and a changed
// registration would inherit an old verdict.
type hookDefinition struct {
	Agent   string   `json:"agent"`
	Event   string   `json:"event"`
	Matcher string   `json:"matcher"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout int      `json:"timeout"`
}

func (d hookDefinition) fingerprint() string {
	raw, err := json.Marshal(d)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// hookRegistration is what one agent's config file says right now.
type hookRegistration struct {
	Agent      string
	ConfigPath string
	Detected   bool // the agent itself is installed on this machine
	Registered bool // and our lookup is in its config

	// Exe and Args are what a smoke check must run to be running what the
	// agent runs. Codex takes its command as one string; it is split back
	// here so both agents are exercised the same way.
	Exe  string
	Args []string

	Definition  hookDefinition
	Fingerprint string

	// Problem says why there is no usable registration, in the user's terms.
	Problem string
	// Unavailable separates "nothing is registered" from "something is, or
	// was attempted, and it cannot work" — a Codex block that could not be
	// given a runnable command, a config we must not touch, a registered
	// command whose binary is gone. The audit asked for exactly this line to
	// be visible, because a failed registration and an absent one need
	// opposite fixes.
	Unavailable bool

	// TrustVerifiable is false for an agent that requires a manual approval
	// it gives us no way to read back.
	TrustVerifiable bool
	TrustAction     string
}

// hookRegistrations reads every agent that gets the build-failure lookup.
// Gemini CLI and OpenCode get the MCP server and the usage rule but no hook,
// so they are not readiness surfaces and are deliberately absent.
func hookRegistrations(agentHome string) []hookRegistration {
	return []hookRegistration{
		claudeHookRegistration(agentHome),
		codexHookRegistration(agentHome),
	}
}

// claudeHookRegistration reads ~/.claude/settings.json.
//
// Ours is the entry whose args are ["hook","agent"] — not the one whose
// command equals this binary. A registration left behind by an older install
// at a path this binary no longer lives at is still OUR registration, and it
// is precisely the case that must not read as absent: it is registered, it is
// a different definition, and the verdict recorded for it has expired.
func claudeHookRegistration(agentHome string) hookRegistration {
	r := hookRegistration{Agent: hookAgentClaude, TrustVerifiable: true}
	dir := filepath.Join(agentHome, ".claude")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return r
	}
	r.Detected = true
	r.ConfigPath = filepath.Join(dir, "settings.json")

	raw, err := os.ReadFile(r.ConfigPath)
	if err != nil {
		r.Problem = "there is no settings.json, so nothing registered the lookup"
		return r
	}
	var m map[string]any
	if err := json.Unmarshal(stripBOM(raw), &m); err != nil {
		r.Problem = "settings.json could not be read, so csx left it untouched and " +
			"registered nothing (" + err.Error() + ")"
		r.Unavailable = true
		return r
	}
	hooks, _ := m["hooks"].(map[string]any)
	events, _ := hooks[failureEvent].([]any)
	for _, e := range events {
		entry, _ := e.(map[string]any)
		if entry == nil {
			continue
		}
		matcher, _ := entry["matcher"].(string)
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			args := hookStringSlice(hm["args"])
			if !isCSXHookArgs(args) {
				continue
			}
			cmd, _ := hm["command"].(string)
			r.Registered = true
			r.Exe, r.Args = cmd, args
			r.Definition = hookDefinition{
				Agent: r.Agent, Event: failureEvent, Matcher: matcher,
				Command: cmd, Args: args, Timeout: hookInt(hm["timeout"]),
			}
			r.Fingerprint = r.Definition.fingerprint()
			return hookCheckExeOnDisk(r)
		}
	}
	r.Problem = "no csx build-failure lookup is registered in settings.json"
	return r
}

// hookCheckExeOnDisk downgrades a registration whose command is not there any
// more. An update that moved or renamed the binary leaves a config entry that
// reads exactly like a working one and cannot run — which is the silent
// failure this whole file exists to make loud.
func hookCheckExeOnDisk(r hookRegistration) hookRegistration {
	if strings.TrimSpace(r.Exe) == "" {
		return r
	}
	if _, err := os.Stat(r.Exe); err == nil {
		return r
	}
	r.Unavailable = true
	r.Problem = "registered, but the command it names is not on disk any more: " + r.Exe
	return r
}

// codexHookRegistration reads the csx-fenced block of ~/.codex/config.toml.
//
// A block with an MCP server and no hook is not a corrupt file: it is what
// codexHookCommand refusing looks like on disk. The installer will not write a
// command containing a space because Codex will not run one, and that refusal
// has to reach the reader as "not registered, and here is why" rather than as
// silence.
func codexHookRegistration(agentHome string) hookRegistration {
	r := hookRegistration{Agent: hookAgentCodex, TrustAction: hookCodexTrustAction}
	dir := filepath.Join(agentHome, ".codex")
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return r
	}
	r.Detected = true
	r.ConfigPath = filepath.Join(dir, "config.toml")

	raw, err := os.ReadFile(r.ConfigPath)
	if err != nil {
		r.Problem = "there is no config.toml, so nothing registered the lookup"
		return r
	}
	block, ok := csxTOMLBlock(string(stripBOM(raw)))
	if !ok {
		r.Problem = "csx has written nothing into config.toml"
		return r
	}
	cmd, matcher, timeout, ok := codexHookInBlock(block)
	if !ok {
		r.Problem = "the csx block registers no hook — this install could not build a " +
			"command Codex will run (Codex refuses a path containing a space)"
		r.Unavailable = true
		return r
	}
	exe, isOurs := strings.CutSuffix(cmd, " hook agent")
	if !isOurs {
		r.Problem = "the registered command is not the csx build-failure lookup: " + cmd
		return r
	}
	r.Registered = true
	r.Exe, r.Args = exe, []string{"hook", "agent"}
	r.Definition = hookDefinition{
		Agent: r.Agent, Event: eventPostToolUse, Matcher: matcher,
		Command: cmd, Timeout: timeout,
	}
	r.Fingerprint = r.Definition.fingerprint()
	return hookCheckExeOnDisk(r)
}

// csxTOMLBlock returns what lies between our marker fences.
func csxTOMLBlock(content string) (string, bool) {
	i := strings.Index(content, tomlBegin)
	if i < 0 {
		return "", false
	}
	rest := content[i+len(tomlBegin):]
	j := strings.Index(rest, tomlEnd)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// codexHookInBlock pulls the hook out of our own fenced block.
//
// This reads only what this installer writes, which is why it is not — and
// must not grow into — a TOML parser. Anything outside the fence belongs to
// the user and is none of our business.
func codexHookInBlock(block string) (cmd, matcher string, timeout int, ok bool) {
	section := ""
	for _, line := range strings.Split(block, "\n") {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		if strings.HasPrefix(s, "[") {
			section = s
			continue
		}
		key, val, found := strings.Cut(s, "=")
		if !found {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch {
		case section == "[[hooks.PostToolUse]]" && key == "matcher":
			matcher = tomlBasicString(val)
		case section == "[[hooks.PostToolUse.hooks]]" && key == "command":
			cmd = tomlBasicString(val)
		case section == "[[hooks.PostToolUse.hooks]]" && key == "timeout":
			timeout, _ = strconv.Atoi(val)
		}
	}
	return cmd, matcher, timeout, cmd != ""
}

// tomlBasicString unquotes a TOML basic string, which shares JSON escaping.
func tomlBasicString(v string) string {
	var s string
	if json.Unmarshal([]byte(v), &s) == nil {
		return s
	}
	return strings.Trim(v, `"`)
}

func isCSXHookArgs(args []string) bool {
	return len(args) == 2 && args[0] == "hook" && args[1] == "agent"
}

func hookStringSlice(v any) []string {
	raw, _ := v.([]any)
	if raw == nil {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		s, _ := e.(string)
		out = append(out, s)
	}
	return out
}

func hookInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// hookCheckFileName is where a smoke check's verdict is kept, under CSX_HOME.
// It is deliberately not in config.json: config is what the user chose, and
// this is what a measurement found.
const hookCheckFileName = "hookcheck.json"

// hookCheckRecord is one proved verdict, bound to the definition it proved.
type hookCheckRecord struct {
	Fingerprint string    `json:"fingerprint"`
	CheckedAt   time.Time `json:"checkedAt"`
	Version     string    `json:"csxVersion"`
	Outcome     string    `json:"outcome"`
}

type hookCheckFile struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Agents        map[string]hookCheckRecord `json:"agents"`
}

// loadHookChecks reads the recorded verdicts. Anything it cannot read is no
// verdict at all: a file from a future csx, or one somebody edited, must
// degrade to "nothing has been proved" rather than to a claim.
func loadHookChecks(home string) map[string]hookCheckRecord {
	raw, err := os.ReadFile(filepath.Join(home, hookCheckFileName))
	if err != nil {
		return nil
	}
	var f hookCheckFile
	if json.Unmarshal(stripBOM(raw), &f) != nil {
		return nil
	}
	return f.Agents
}

func saveHookCheck(home, agent string, rec hookCheckRecord) error {
	return writeHookChecks(home, func(m map[string]hookCheckRecord) { m[agent] = rec })
}

// clearHookCheck retires an agent's verdict.
//
// A check that just failed is the freshest thing anyone knows, and an older
// pass against the same definition must not go on speaking over it. The pass
// really happened; it is simply no longer safe to present as the current
// state, which is the same rule a definition change follows.
func clearHookCheck(home, agent string) error {
	if _, ok := loadHookChecks(home)[agent]; !ok {
		return nil
	}
	return writeHookChecks(home, func(m map[string]hookCheckRecord) { delete(m, agent) })
}

func writeHookChecks(home string, mutate func(map[string]hookCheckRecord)) error {
	f := hookCheckFile{SchemaVersion: 1, Agents: loadHookChecks(home)}
	if f.Agents == nil {
		f.Agents = map[string]hookCheckRecord{}
	}
	mutate(f.Agents)
	out, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(home, hookCheckFileName), append(out, '\n'), 0o600)
}

// hookReadiness is one agent's line in the answer.
type hookReadiness struct {
	Agent  string
	State  string
	Detail string
	Config string
	Next   []string
}

// hookReadinessOf resolves the one honest verdict for an agent.
func hookReadinessOf(reg hookRegistration, rec hookCheckRecord, haveRec, off bool) hookReadiness {
	r := hookReadiness{Agent: reg.Agent, Config: reg.ConfigPath}
	switch {
	case !reg.Detected:
		// Not a failure to report. This is simply not one of this user's
		// agents, and an install that never happened has nothing to be ready.
		r.State, r.Config = hookStateNotDetected, ""
		r.Detail = "this agent is not installed on this machine"
		return r
	case reg.Unavailable:
		// Registered-and-broken, or attempted-and-refused. Either way there is
		// no working hook, and "registered" would be a lie of omission: the
		// two need opposite fixes and only one of them is re-running init.
		r.State, r.Detail = hookStateUnavailable, reg.Problem
		r.Next = []string{"csx init — re-run the installer to register a working lookup"}
		return r
	case !reg.Registered:
		r.State, r.Detail = hookStateNotRegistered, reg.Problem
		r.Next = []string{"csx init — re-run the installer to register the lookup"}
		return r
	case off:
		// Registered, and switched off on our side. Reporting the
		// registration without this would show a line that looks live.
		r.State = hookStateOff
		r.Detail = "registered, but the lookup is switched off in csx"
		r.Next = []string{"csx hook on — turn the lookup back on"}
		return r
	}

	switch {
	case haveRec && rec.Fingerprint == reg.Fingerprint:
		r.State = hookStateVerified
		r.Detail = fmt.Sprintf("a failing build reached this hook on %s (csx %s)",
			rec.CheckedAt.UTC().Format(time.RFC3339), rec.Version)
	case haveRec:
		// The acceptance criterion. The verdict was about a definition that
		// is no longer the one registered, so it says nothing about now —
		// and on Codex it means the trust that was granted was granted to a
		// hook that no longer exists.
		r.State = hookStateRegistered
		r.Detail = fmt.Sprintf("the hook definition changed since it was last proved on %s, "+
			"so that check no longer describes what is registered",
			rec.CheckedAt.UTC().Format(time.RFC3339))
		r.Next = append(r.Next, "csx hook check — prove the definition that is registered now")
	default:
		r.State = hookStateRegistered
		r.Detail = "in the agent's config; nothing has yet proved a failing build reaches it"
		r.Next = append(r.Next, "csx hook check — prove a failing build reaches it")
	}
	if !reg.TrustVerifiable {
		r.Detail += " — " + hookApprovalNotVerifiable
		r.Next = append(r.Next, reg.TrustAction)
	}
	return r
}

// hookReadinessAll answers for every agent that gets the lookup.
func hookReadinessAll(env *hookReadyEnv, off bool) []hookReadiness {
	recs := loadHookChecks(env.home)
	regs := hookRegistrations(env.agentHome)
	out := make([]hookReadiness, 0, len(regs))
	for _, reg := range regs {
		rec, have := recs[reg.Agent]
		out = append(out, hookReadinessOf(reg, rec, have, off))
	}
	return out
}

func renderHookReadiness(w io.Writer, state string, list []hookReadiness) {
	fmt.Fprintln(w, "build-failure lookup: "+state)
	if state == hookStateOff {
		fmt.Fprintln(w, "  csx hook on — turn it back on")
	}
	fmt.Fprintln(w)
	for _, r := range list {
		fmt.Fprintf(w, "  %-12s %s — %s\n", r.Agent, r.State, r.Detail)
		if r.Config != "" {
			fmt.Fprintf(w, "  %-12s config: %s\n", "", r.Config)
		}
		for _, n := range r.Next {
			fmt.Fprintf(w, "  %-12s next:   %s\n", "", n)
		}
	}
}

// hookSmokeResult is what one smoke check established.
type hookSmokeResult struct {
	// Code is the hook's own trace code — how far the failure got.
	Code string
	// Reached means the failing build travelled the whole path: the agent's
	// registered command ran it, the hook recognised it as a failed build
	// step, and the lookup was asked. Whether the network had an answer is a
	// different question and not this one.
	Reached bool
	Detail  string
	Err     error
}

type hookSmokeFunc func(ctx context.Context, reg hookRegistration) hookSmokeResult

// hookSmokeBudget bounds one check. The lookup inside it already has
// hookSearchBudget, and this has to outlast that or the check would report a
// timeout it caused itself.
const hookSmokeBudget = hookSearchBudget + 20*time.Second

// The synthetic failure. It names itself, so anyone who ever sees it in a log
// knows no build ran.
const (
	hookSmokeCommand   = "npm run build"
	hookSmokeError     = "npm ERR! csx hook check: synthetic build failure — no real build ran"
	hookSmokeToolUseID = "csx-hook-check"
)

// hookSmokeProject builds the throwaway project the check runs against, and
// the failure event to feed the hook.
//
// A throwaway project, never a real one: the check must be safe to run in any
// directory, at any time, and it must not depend on the user having a broken
// build lying around. package.json with no dependencies is exactly enough —
// the scanner detects an ecosystem, the classifier calls `npm run build` a
// build step, and a lookup that misses has no public package to file a wanted
// row about.
func hookSmokeProject(agent string) (dir string, payload []byte, cleanup func(), err error) {
	cleanup = func() {}
	dir, err = os.MkdirTemp("", "csx-hook-check-")
	if err != nil {
		return "", nil, cleanup, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	pkg := `{"name":"csx-hook-check","private":true,"version":"0.0.0","scripts":{"build":"exit 1"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o600); err != nil {
		return dir, nil, cleanup, err
	}

	p := map[string]any{
		"tool_name":   "Bash",
		"cwd":         dir,
		"tool_use_id": hookSmokeToolUseID,
		"tool_input":  map[string]any{"command": hookSmokeCommand},
	}
	switch agent {
	case hookAgentCodex:
		// Codex has no failure-only event, and its payload carries no exit
		// code: the hook recovers one from the rollout file the event names.
		// So the check has to write that file — otherwise it would prove the
		// transport and skip the half of the path that is Codex-specific.
		rollout := filepath.Join(dir, "rollout.jsonl")
		line, merr := json.Marshal(map[string]any{"payload": map[string]any{"item": map[string]any{
			"type": "CommandExecution", "id": hookSmokeToolUseID, "exit_code": 1,
		}}})
		if merr != nil {
			return dir, nil, cleanup, merr
		}
		if err := os.WriteFile(rollout, append(line, '\n'), 0o600); err != nil {
			return dir, nil, cleanup, err
		}
		p["hook_event_name"] = eventPostToolUse
		p["transcript_path"] = rollout
		p["tool_response"] = "Exit code 1\n" + hookSmokeError
	default:
		p["hook_event_name"] = eventClaudeFailure
		p["error"] = "Exit code 1\n" + hookSmokeError
	}
	payload, err = json.Marshal(p)
	return dir, payload, cleanup, err
}

// hookSmoke runs the exact command the agent is registered to run, feeds it a
// failing build in a throwaway project, and reports how far it got.
//
// It cannot prove the agent will invoke it — no agent exposes that — so it
// proves everything up to that line and says so.
func hookSmoke(ctx context.Context, reg hookRegistration) hookSmokeResult {
	_, payload, cleanup, err := hookSmokeProject(reg.Agent)
	defer cleanup()
	if err != nil {
		return hookSmokeResult{Err: err, Detail: "the check could not build a throwaway project: " + err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, hookSmokeBudget)
	defer cancel()
	cmd := exec.CommandContext(ctx, reg.Exe, reg.Args...)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Silence is this hook's normal answer, so silence is no evidence at all.
	// CSX_HOOK_DEBUG is what makes it say how far it got, and that trace is
	// the whole basis of this verdict.
	cmd.Env = append(os.Environ(), "CSX_HOOK_DEBUG=1")

	if err := cmd.Run(); err != nil {
		return hookSmokeResult{
			Err:    err,
			Code:   hookTraceCode(stderr.String()),
			Detail: "the registered command did not run (" + err.Error() + ")",
		}
	}

	code := hookTraceCode(stderr.String())
	if hookAnswered(stdout.Bytes()) {
		code = hookTraceAnswered
	}
	reached, detail := hookSmokeVerdict(code)
	return hookSmokeResult{Code: code, Reached: reached, Detail: detail}
}

// hookSmokeVerdict turns the hook's own trace code into the sentence a reader
// needs, and into the single yes/no this check exists to answer.
func hookSmokeVerdict(code string) (bool, string) {
	switch code {
	case hookTraceAnswered:
		return true, "a failing build reached the lookup and it answered"
	case hookTraceNoMatch:
		return true, "a failing build reached the lookup; this network has nothing proved for " +
			"that error, which is the honest answer and not a wiring fault"
	case hookTraceUnrelated, hookTraceLowRelevance:
		// A deliberate decision, not a wiring fault. The failure reached the
		// lookup, the lookup answered, and the relevance gate declined to
		// interrupt with what came back — which is the gate working. Left in
		// the default branch these read as "the hook stopped early", and a
		// working install would be reported as broken.
		return true, "a failing build reached the lookup; what came back was not about that " +
			"failure (" + code + "), so the hook stayed quiet — which is the answer, not a fault"
	case hookTraceSearchFailed:
		return false, "the failure reached the hook, but the lookup could not complete — " +
			"try `csx daemon start`"
	case hookTraceOff:
		return false, "the lookup is switched off (`csx hook on`)"
	case hookTraceNotBuildStep:
		return false, "the failure reached the hook but was not recognised as a build step"
	case hookTraceNotFailed:
		return false, "the hook did not read the synthetic event as a failure"
	case "":
		return false, "the registered command printed no trace, so nothing can be said about it"
	default:
		return false, "the hook stopped early (" + code + ")"
	}
}

// hookAnswered reports whether the hook handed the agent an answer.
func hookAnswered(stdout []byte) bool {
	if len(bytes.TrimSpace(stdout)) == 0 {
		return false
	}
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(stdout, &got) != nil {
		return false
	}
	return strings.TrimSpace(got.HookSpecificOutput.AdditionalContext) != ""
}

// hookReadyEnv is every host dependency the readiness commands touch, so a
// test can point them at throwaway homes and a scripted smoke check.
type hookReadyEnv struct {
	home         string // CSX_HOME: where the verdict is recorded
	agentHome    string // the root every agent config path resolves under
	agentHomeErr error
	overrode     bool
	now          func() time.Time
	version      string
	smoke        hookSmokeFunc
}

func newHookReadyEnv() (*hookReadyEnv, error) {
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	agentHome, overrode, aerr := resolveAgentHome(nil)
	return &hookReadyEnv{
		home:         home,
		agentHome:    agentHome,
		agentHomeErr: aerr,
		overrode:     overrode,
		now:          time.Now,
		version:      Version,
		smoke:        hookSmoke,
	}, nil
}

// hookStatusMain answers "is it actually wired up", from what is on disk.
func hookStatusMain(env *hookReadyEnv, cfg *config.Config, stdout, stderr io.Writer) int {
	if env.agentHomeErr != nil {
		fmt.Fprintln(stdout, "build-failure lookup: "+hookState(cfg))
		fmt.Fprintf(stderr, "csx: cannot read agent configs: %v\n", env.agentHomeErr)
		return 1
	}
	state := hookState(cfg)
	renderHookReadiness(stdout, state, hookReadinessAll(env, state == hookStateOff))
	if env.overrode {
		fmt.Fprintf(stdout, "\n  (agent home overridden by %s: %s)\n", agentHomeEnv, env.agentHome)
	}
	return 0
}

// hookCheckMain proves it, or says what it could not prove.
func hookCheckMain(ctx context.Context, env *hookReadyEnv, cfg *config.Config, stdout, stderr io.Writer) int {
	if env.agentHomeErr != nil {
		fmt.Fprintf(stderr, "csx: cannot read agent configs: %v\n", env.agentHomeErr)
		return 1
	}
	if hookState(cfg) == hookStateOff {
		fmt.Fprintln(stdout, "build-failure lookup: off — nothing to check.")
		fmt.Fprintln(stdout, "  csx hook on — turn it back on, then run this again")
		return 1
	}

	fmt.Fprintln(stdout, "Running a failing build through each registered hook.")
	fmt.Fprintln(stdout, "Nothing outside a throwaway temporary project is touched.")
	fmt.Fprintln(stdout)

	checked, failed := 0, 0
	for _, reg := range hookRegistrations(env.agentHome) {
		switch {
		case !reg.Detected:
			fmt.Fprintf(stdout, "  %-12s not detected — skipped\n", reg.Agent)
			continue
		case reg.Unavailable:
			fmt.Fprintf(stdout, "  %-12s unavailable — %s\n", reg.Agent, reg.Problem)
			failed++
			continue
		case !reg.Registered:
			fmt.Fprintf(stdout, "  %-12s not registered — %s\n", reg.Agent, reg.Problem)
			failed++
			continue
		}
		fmt.Fprintf(stdout, "  %-12s checking...\n", reg.Agent)
		res := env.smoke(ctx, reg)
		checked++
		if !res.Reached {
			failed++
			fmt.Fprintf(stdout, "  %-12s NOT PROVED — %s\n", "", res.Detail)
			// And the last pass stops counting. Leaving it would let a
			// verdict from before whatever just broke answer for today.
			if err := clearHookCheck(env.home, reg.Agent); err != nil {
				fmt.Fprintf(stderr, "csx: could not retire the previous check: %v\n", err)
			}
			continue
		}
		fmt.Fprintf(stdout, "  %-12s proved — %s\n", "", res.Detail)
		if err := saveHookCheck(env.home, reg.Agent, hookCheckRecord{
			Fingerprint: reg.Fingerprint,
			CheckedAt:   env.now().UTC(),
			Version:     env.version,
			Outcome:     res.Code,
		}); err != nil {
			// The check happened; only the record failed. Say so rather than
			// letting a later status quietly claim nothing was ever proved.
			fmt.Fprintf(stderr, "csx: the check passed but could not be recorded: %v\n", err)
			failed++
		}
	}

	fmt.Fprintln(stdout)
	hookStatusMain(env, cfg, stdout, stderr)
	if checked == 0 && failed == 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "No agent that takes the build-failure lookup is installed here.")
		return 1
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// hookReadinessFollowUp is the durable pointer an install owes the reader.
//
// The install screen scrolls away and takes its promises with it. Whether the
// lookup is actually wired up has to stay answerable afterwards, from a
// command the reader was told about while they were still looking.
func hookReadinessFollowUp(results []agentInstallResult) string {
	for _, r := range results {
		if r.Skipped || len(r.Actions) == 0 {
			continue
		}
		if r.Agent == hookAgentClaude || r.Agent == hookAgentCodex {
			return "Whether the lookup is wired up stays answerable after this screen:\n" +
				"    csx hook status   what each agent registered, and what has been proved of it\n" +
				"    csx hook check    run a failing build through it, in a throwaway project"
		}
	}
	return ""
}
