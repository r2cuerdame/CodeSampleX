package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
)

// The build-failure hook.
//
// An agent that hits a compile error already has a fix it believes in, so it
// does not stop to ask: six searches reached the server in a week while 648
// misses did. run_observed_command asks on its own now, but only for builds
// that went through it — and an agent mostly runs its build in a shell.
//
// This is the same answer for that path. The agent's own harness tells us a
// command failed; we decide whether it was a build step, ask, and hand back
// what the network knows. Nobody has to remember anything.
func init() {
	Register(Command{
		Name:    "hook",
		Summary: "the build-failure lookup installed into coding agents: on | off | status | check",
		Run: func(ctx context.Context, args []string) int {
			if len(args) > 0 && args[0] == "agent" {
				return hookAgentMain(ctx, defaultHookEnv())
			}
			env, err := newHookReadyEnv()
			if err != nil {
				fmt.Fprintln(os.Stderr, "csx: "+err.Error())
				return 1
			}
			return hookSwitchMain(ctx, args, os.Stdout, os.Stderr, env)
		},
	})
}

// hookProject is what the project says about the command that failed: whether
// it was a build step at all, and the context a search needs.
type hookProject struct {
	Known bool
	Stage domain.Stage
	// Argv is the segment the classifier recognised as the build step, out
	// of everything the agent's shell line ran. The relevance gate needs the
	// tool that failed, not the whole line: "cd api && npm test" is an npm
	// failure, and reading "cd" off the front makes it belong to no
	// ecosystem at all.
	Argv     []string
	Env      domain.EnvironmentFingerprint
	Packages []string
	Symbols  []string
}

type hookEnv struct {
	stdin   io.Reader
	stdout  io.Writer
	debug   io.Writer // non-nil only when CSX_HOOK_DEBUG is set
	cfg     func() (*config.Config, error)
	inspect func(ctx context.Context, cwd string, segments [][]string) hookProject
	search  func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, error)
}

// hookPayload is the part of the agent's failure event we read. The rest of
// it is none of our business.
// These field names were measured against a real session, not taken from the
// documentation. The documented example for the neighbouring PostToolUse
// event carries its result in tool_output.text, and a hook written to that
// shape would have found nothing on every failure and stayed silent forever —
// which is indistinguishable from working.
//
// A real PostToolUseFailure event has no tool_output at all. It has `error`,
// and that one string carries both halves:
//
//	"Exit code 1\nERR_MODULE_NOT_FOUND: cannot find module widget"
//
// It also has to cover Codex, whose event is a different shape again: no
// failure-only event, no exit code, and the output in tool_response. See
// hookcodex.go — the two are reconciled in hookFailureText.
type hookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	ToolName       string `json:"tool_name"`
	CWD            string `json:"cwd"`
	ToolUseID      string `json:"tool_use_id"`
	TranscriptPath string `json:"transcript_path"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	Error        string `json:"error"`
	ToolResponse string `json:"tool_response"`
}

// hookErrorLines bounds how much of the failed command's output becomes the
// question. The tail is where the error is, and a whole build log is neither
// a better question nor a polite thing to hand a model.
const hookErrorLines = 40

// hookAgentMain answers one failed tool call. It returns 0 always: this runs
// inside somebody's editing session, and a hook that fails a session it was
// only meant to help has done more harm than the lookup was ever worth.
func hookAgentMain(ctx context.Context, env *hookEnv) int {
	// Silence is this hook's normal answer, which makes "it said nothing"
	// impossible to tell apart from "it is broken". Setting CSX_HOOK_DEBUG
	// makes it say why, on stderr, where the model never looks.
	// The code in front of the sentence is what `csx hook check` reads back:
	// a hook that stayed quiet and a hook that was never reached look
	// identical from outside, and the trace is the only thing that tells
	// them apart.
	quiet := func(code, why string) int {
		if env.debug != nil {
			fmt.Fprintf(env.debug, "csx hook: [%s] %s\n", code, why)
		}
		return 0
	}

	var p hookPayload
	if err := json.NewDecoder(env.stdin).Decode(&p); err != nil {
		return quiet(hookTraceBadInput, "input is not the JSON this hook expects: "+err.Error())
	}
	// Only shell commands. The agent's own tools do not fail this way, and
	// the classifier has nothing to say about them.
	if p.ToolName != "Bash" || strings.TrimSpace(p.ToolInput.Command) == "" {
		return quiet(hookTraceNotBash, "not a shell command (tool "+p.ToolName+")")
	}
	cfg, err := env.cfg()
	if err != nil {
		return quiet(hookTraceNoConfig, "cannot read config: "+err.Error())
	}
	if cfg == nil || strings.EqualFold(strings.TrimSpace(cfg.FailureHook), "off") {
		return quiet(hookTraceOff, "turned off (csx hook on)")
	}

	// Did it fail, and what did it say? Asked FIRST, before the project is
	// scanned, because Codex raises this event after every command it runs —
	// successes included — and scanning a tree to decide whether to stay
	// quiet is work done on every green build in the session.
	errText, failed := hookFailureText(p)
	if !failed {
		return quiet(hookTraceNotFailed, "the command did not fail, or the failure could not be confirmed")
	}

	segments := hookSegments(p.ToolInput.Command)
	if len(segments) == 0 {
		return quiet(hookTraceNoSegments, "nothing to classify in "+strconv.Quote(p.ToolInput.Command))
	}
	proj := env.inspect(ctx, p.CWD, segments)
	// Scope. A failed `git status` is not something this network knows about,
	// and the test of what counts is the same classifier that decides which
	// evidence stage a wrapped command records — one definition of "a build
	// step", not two that drift apart.
	if !proj.Known {
		return quiet(hookTraceNotBuildStep, "not a build step: "+strconv.Quote(p.ToolInput.Command)+
			" in "+strconv.Quote(p.CWD))
	}

	req := domain.SearchRequest{
		SchemaVersion:   2,
		Query:           errText,
		ProjectPackages: proj.Packages,
		// Scanner-derived, so ranking-only. These symbols were not asked
		// about by anyone and must never exclude a candidate.
		Symbols:               proj.Symbols,
		ContextSymbols:        proj.Symbols,
		SymbolProvenance:      domain.SearchProvenanceContext,
		Environment:           proj.Env,
		EnvironmentProvenance: domain.SearchProvenanceContext,
		// No Limit. Asking for one result is not the same question as asking
		// for three and reading the first: from a project where `csx search`
		// answered, the same query with Limit 1 missed. Brevity is a
		// rendering decision and is made below, after the engine has had the
		// whole candidate set to judge.
	}
	if req.Query == "" {
		return quiet(hookTraceEmptyQuery, "the failed command printed nothing to ask about")
	}
	resp, err := env.search(ctx, req)
	// Silence on a miss, and silence when the daemon is down. An agent
	// interrupted to be told nothing was found learns to resent the
	// interruption, and the network being unreachable is not the build's
	// problem.
	if err != nil {
		return quiet(hookTraceSearchFailed, "the search did not complete: "+err.Error())
	}
	if resp.Miss || len(resp.Results) == 0 {
		// The question, not just the verdict. "Nothing was found" and "we
		// asked the wrong question" look identical from outside, and the two
		// need opposite fixes.
		asked, _ := json.Marshal(req)
		return quiet(hookTraceNoMatch, "nothing here has been proven for this failure; asked: "+string(asked))
	}

	// One answer. This arrives unasked, in the middle of somebody's work, so
	// it earns a few lines and not a list.
	if len(resp.Results) > 1 {
		resp.Results = resp.Results[:1]
	}
	// The same relevance gate the MCP lookup applies, and the same one on
	// purpose: two definitions of "this is the wrong language" drift, and
	// this path and that one answer the same agent about the same build.
	//
	// Here the demotion is silence rather than a shortened note. This hook
	// interrupts; a note nobody asked for, about a language the failing
	// command does not build for, costs the reader more than saying nothing
	// and teaches them to stop reading the ones that matter.
	if resp.Results[0].UnrelatedToCommand(proj.Argv) {
		return quiet(hookTraceUnrelated, "the only match is a "+
			strings.Join(resp.Results[0].SampleEcosystems(), "/")+
			" sample and the failed command does not build for it: "+resp.Results[0].SampleID)
	}
	classification, advisoryOnly, reason := resp.Results[0].RecommendationClassification()
	var b strings.Builder
	b.WriteString("CODESAMPLEX SUPPORTING INFORMATION — SECONDARY\n")
	fmt.Fprintf(&b, "CLASSIFICATION: %s\n", classification)
	if advisoryOnly {
		b.WriteString("ADVISORY ONLY: reference candidate; not an automatic fix basis.\n")
	}
	if reason != "" {
		b.WriteString("Reason: " + reason + "\n")
	}
	fmt.Fprintf(&b, "Observed stage: %s\n\n", proj.Stage)
	renderSearchText(&b, resp)
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     p.HookEventName,
			"additionalContext": b.String(),
		},
	})
	if err != nil {
		return quiet(hookTraceEncodeFailed, "could not encode the answer: "+err.Error())
	}
	_, _ = env.stdout.Write(out)
	if env.debug != nil {
		fmt.Fprintf(env.debug, "csx hook: [%s] %s\n", hookTraceAnswered, resp.Results[0].SampleID)
	}
	return 0
}

// exitCodeLine is the line the agent puts in front of the command's output.
var exitCodeLine = regexp.MustCompile(`^Exit code [0-9]+\s*$`)

// hookQuery turns the failure into the question.
//
// The tail is where the error is. The "Exit code 1" line in front of it is
// not part of the error — it is the same sentence on every failure in every
// ecosystem, so it can only blur a search, never sharpen one.
//
// Nothing is sanitized here. The sanitizer is for what LEAVES the machine,
// and this question is answered by shards sitting on it.
func hookQuery(errText string) string {
	lines := strings.Split(strings.TrimRight(errText, "\n"), "\n")
	if len(lines) > 0 && exitCodeLine.MatchString(lines[0]) {
		lines = lines[1:]
	}
	if len(lines) > hookErrorLines {
		lines = lines[len(lines)-hookErrorLines:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// hookSegments splits a shell line into the commands it runs.
//
// The agent writes a line, not an argv, and "cd api && npm test" is a build
// step with a chore in front of it — reading only the first word would miss
// every command written that way. Splitting on whitespace is not enough
// either, because nobody spaces their operators out: "npm ci&&npm run build"
// is one word to strings.Fields.
//
// Quotes are honoured only far enough to stop a separator inside one from
// inventing a command nobody ran. This is not a shell parser and does not
// need to be: its whole job is to find the words a build command starts with.
func hookSegments(command string) [][]string {
	var (
		out   [][]string
		cur   []string
		word  strings.Builder
		quote rune
	)
	endWord := func() {
		if word.Len() > 0 {
			cur = append(cur, word.String())
			word.Reset()
		}
	}
	endCommand := func() {
		endWord()
		if len(cur) > 0 {
			out = append(out, cur)
			cur = nil
		}
	}

	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			word.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			word.WriteRune(c)
		case c == '&' || c == '|':
			// && and || are one separator; a single & or | is another. Both
			// end the command either way.
			if i+1 < len(runes) && runes[i+1] == c {
				i++
			}
			endCommand()
		case c == ';' || c == '\n':
			endCommand()
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		default:
			word.WriteRune(c)
		}
	}
	endCommand()
	return out
}

// hookSwitchMain is the switch, and the answer to whether the thing it
// switches is actually wired up.
//
// The switch alone was never enough. "on" is a statement about a config flag:
// it is true of a machine where the hook was never registered, of one where an
// update rewrote the definition Codex was told to trust, and of one where the
// registration could not be created at all. See hookready.go.
func hookSwitchMain(ctx context.Context, args []string, stdout, stderr io.Writer, env *hookReadyEnv) int {
	cfg, err := config.Load(env.home)
	if err != nil {
		fmt.Fprintln(stderr, "csx: "+err.Error())
		return 1
	}

	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "on", "off":
		cfg.FailureHook = sub
		if err := cfg.Save(env.home); err != nil {
			fmt.Fprintln(stderr, "csx: "+err.Error())
			return 1
		}
		fmt.Fprintln(stdout, "build-failure lookup: "+sub)
		return 0
	case "status":
		return hookStatusMain(env, cfg, stdout, stderr)
	case "check":
		return hookCheckMain(ctx, env, cfg, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: csx hook [on|off|status|check]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "  status  what each agent's config says, and what has been proved about it")
		fmt.Fprintln(stderr, "  check   run a failing build through every registered hook, in a")
		fmt.Fprintln(stderr, "          throwaway project, and record what it proved")
		return 2
	}
}

// hookTrace* are the stable codes the hook prints when CSX_HOOK_DEBUG is set.
//
// They exist because this hook's normal answer is silence, which makes "it
// found nothing" indistinguishable from "it never ran" — and telling those two
// apart is the entire job of `csx hook check`. The sentences beside them are
// for a human and may be reworded; these are read by code and may not.
const (
	hookTraceAnswered     = "answered"
	hookTraceBadInput     = "bad-input"
	hookTraceNotBash      = "not-bash"
	hookTraceNoConfig     = "no-config"
	hookTraceOff          = "off"
	hookTraceNotFailed    = "not-failed"
	hookTraceNoSegments   = "no-segments"
	hookTraceNotBuildStep = "not-build-step"
	hookTraceEmptyQuery   = "empty-query"
	hookTraceSearchFailed = "search-failed"
	hookTraceNoMatch      = "no-match"
	hookTraceUnrelated    = "unrelated-ecosystem"
	hookTraceEncodeFailed = "encode-failed"
)

// hookTraceCodes is every code the hook can emit, so a test can hold the
// vocabulary the check reads and the one the hook writes to the same list.
var hookTraceCodes = []string{
	hookTraceAnswered, hookTraceBadInput, hookTraceNotBash, hookTraceNoConfig,
	hookTraceOff, hookTraceNotFailed, hookTraceNoSegments, hookTraceNotBuildStep,
	hookTraceEmptyQuery, hookTraceSearchFailed, hookTraceNoMatch, hookTraceUnrelated,
	hookTraceEncodeFailed,
}

// hookTracePrefix is what every trace line starts with.
const hookTracePrefix = "csx hook: ["

// hookTraceCode reads the last code out of a hook's debug output. The last
// one, because a single run prints at most one — but a caller that captured
// more than one run must be told how the last one ended.
func hookTraceCode(stderr string) string {
	code := ""
	for _, line := range strings.Split(stderr, "\n") {
		i := strings.Index(line, hookTracePrefix)
		if i < 0 {
			continue
		}
		rest := line[i+len(hookTracePrefix):]
		if j := strings.IndexByte(rest, ']'); j >= 0 {
			code = rest[:j]
		}
	}
	return code
}

func hookState(cfg *config.Config) string {
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.FailureHook), "off") {
		return "off"
	}
	return "on"
}

func defaultHookEnv() *hookEnv {
	var debug io.Writer
	if os.Getenv("CSX_HOOK_DEBUG") != "" {
		debug = os.Stderr
	}
	return &hookEnv{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		debug:  debug,
		cfg: func() (*config.Config, error) {
			home, err := config.Home()
			if err != nil {
				return nil, err
			}
			return config.Load(home)
		},
		inspect: func(ctx context.Context, cwd string, segments [][]string) hookProject {
			if strings.TrimSpace(cwd) == "" {
				return hookProject{}
			}
			// No publicness checker: a search uploads nothing, and this runs
			// on somebody's keystroke.
			res, err := evidence.Scan(ctx, cwd, nil)
			if err != nil || res == nil {
				return hookProject{}
			}
			p := hookProject{
				Env:      res.Env,
				Packages: projectPackages(res),
				Symbols:  projectSymbols(res),
			}
			for _, argv := range segments {
				if prof := res.Classify(argv); prof.Known {
					p.Known, p.Stage, p.Argv = true, prof.Stage, argv
					break
				}
			}
			return p
		},
		search: func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, error) {
			home, err := config.Home()
			if err != nil {
				return domain.SearchResponse{}, err
			}
			sctx, cancel := context.WithTimeout(ctx, hookSearchBudget)
			defer cancel()
			resp, derr := searchViaDaemon(sctx, home, req)
			if derr == nil {
				return resp.SearchResponse, nil
			}
			// A running daemon that did not answer in time is NOT a reason to
			// open a second engine on the same home. It cannot take the lock
			// the daemon holds, and what it returns is worse than nothing:
			// measured on this machine, the daemon answered a query the
			// second engine called a miss. Silence is the honest result.
			if daemonAlive(ctx, home) {
				return domain.SearchResponse{}, derr
			}
			// No daemon: ask the local engine directly, and RECORD it. A
			// search the instrumentation cannot see is the problem this hook
			// exists to fix, so the hook must not make more of them.
			d, err := daemon.New(home)
			if err != nil {
				return domain.SearchResponse{}, err
			}
			defer d.Close()
			return d.SearchAndRecord(sctx, req).SearchResponse, nil
		},
	}
}

// firstLine keeps a debug line to one line.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// hookSearchBudget is how long the lookup may take before it gives up.
//
// It has to fit inside the timeout written into the agent's own hook entry,
// and it is far longer than a lookup ought to need. Measured on the machine
// this was built on, a daemon-backed search of one build error took 13 to 19
// seconds; an 8-second budget turned every one of them into silence. The
// number is set to the truth about how long searches currently take, not to
// how long they should take, and the second is the thing worth fixing.
const hookSearchBudget = 25 * time.Second

// daemonAlive reports whether the local daemon is answering at all, which is
// a different question from whether it answered THIS search in time.
func daemonAlive(ctx context.Context, home string) bool {
	c, err := daemon.NewClient(home)
	if err != nil {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err = c.Status(pctx)
	return err == nil
}
