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
		Summary: "the build-failure lookup installed into coding agents: on | off | status",
		Run: func(ctx context.Context, args []string) int {
			if len(args) > 0 && args[0] == "agent" {
				return hookAgentMain(ctx, defaultHookEnv())
			}
			return hookSwitchMain(args, os.Stdout, os.Stderr)
		},
	})
}

// hookProject is what the project says about the command that failed: whether
// it was a build step at all, and the context a search needs.
type hookProject struct {
	Known    bool
	Stage    domain.Stage
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
type hookPayload struct {
	ToolName  string `json:"tool_name"`
	CWD       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	Error string `json:"error"`
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
	quiet := func(why string) int {
		if env.debug != nil {
			fmt.Fprintln(env.debug, "csx hook: silent — "+why)
		}
		return 0
	}

	var p hookPayload
	if err := json.NewDecoder(env.stdin).Decode(&p); err != nil {
		return quiet("input is not the JSON this hook expects: " + err.Error())
	}
	// Only shell commands. The agent's own tools do not fail this way, and
	// the classifier has nothing to say about them.
	if p.ToolName != "Bash" || strings.TrimSpace(p.ToolInput.Command) == "" {
		return quiet("not a shell command (tool " + p.ToolName + ")")
	}
	cfg, err := env.cfg()
	if err != nil {
		return quiet("cannot read config: " + err.Error())
	}
	if cfg == nil || strings.EqualFold(strings.TrimSpace(cfg.FailureHook), "off") {
		return quiet("turned off (csx hook on)")
	}

	segments := hookSegments(p.ToolInput.Command)
	if len(segments) == 0 {
		return quiet("nothing to classify in " + strconv.Quote(p.ToolInput.Command))
	}
	proj := env.inspect(ctx, p.CWD, segments)
	// Scope. A failed `git status` is not something this network knows about,
	// and the test of what counts is the same classifier that decides which
	// evidence stage a wrapped command records — one definition of "a build
	// step", not two that drift apart.
	if !proj.Known {
		return quiet("not a build step: " + strconv.Quote(p.ToolInput.Command) +
			" in " + strconv.Quote(p.CWD))
	}

	req := domain.SearchRequest{
		SchemaVersion:   2,
		Query:           hookQuery(p.Error),
		ProjectPackages: proj.Packages,
		// Scanner-derived, so ranking-only. These symbols were not asked
		// about by anyone and must never exclude a candidate.
		Symbols:               proj.Symbols,
		ContextSymbols:        proj.Symbols,
		SymbolProvenance:      domain.SearchProvenanceContext,
		Environment:           proj.Env,
		EnvironmentProvenance: domain.SearchProvenanceContext,
		Limit:                 1,
	}
	if req.Query == "" {
		return quiet("the failed command printed nothing to ask about")
	}
	resp, err := env.search(ctx, req)
	// Silence on a miss, and silence when the daemon is down. An agent
	// interrupted to be told nothing was found learns to resent the
	// interruption, and the network being unreachable is not the build's
	// problem.
	if err != nil {
		return quiet("the search did not complete: " + err.Error())
	}
	if resp.Miss || len(resp.Results) == 0 {
		return quiet("nothing here has been proven for this failure")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CodeSampleX looked this failure up automatically (%s).\n\n", proj.Stage)
	renderSearchText(&b, resp)
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUseFailure",
			"additionalContext": b.String(),
		},
	})
	if err != nil {
		return quiet("could not encode the answer: " + err.Error())
	}
	_, _ = env.stdout.Write(out)
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

// hookSwitchMain is the one switch a reader ever touches.
func hookSwitchMain(args []string, stdout, stderr io.Writer) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintln(stderr, "csx: "+err.Error())
		return 1
	}
	cfg, err := config.Load(home)
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
		if err := cfg.Save(home); err != nil {
			fmt.Fprintln(stderr, "csx: "+err.Error())
			return 1
		}
		fmt.Fprintln(stdout, "build-failure lookup: "+sub)
		return 0
	case "status":
		fmt.Fprintln(stdout, "build-failure lookup: "+hookState(cfg))
		return 0
	default:
		fmt.Fprintln(stderr, "usage: csx hook [on|off|status]")
		return 2
	}
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
					p.Known, p.Stage = true, prof.Stage
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
			// Short. This sits on the critical path of somebody's session.
			sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			if resp, err := searchViaDaemon(sctx, home, req); err == nil {
				return resp.SearchResponse, nil
			}
			// Daemon down: ask the local engine directly, and RECORD it.
			// A search the instrumentation cannot see is the problem this
			// hook exists to fix, so the hook must not create more of them.
			d, err := daemon.New(home)
			if err != nil {
				return domain.SearchResponse{}, err
			}
			defer d.Close()
			return d.SearchAndRecord(sctx, req).SearchResponse, nil
		},
	}
}
