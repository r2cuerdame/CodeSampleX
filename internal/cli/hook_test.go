package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// hookInput builds the event the agent actually sends. The shape here was
// measured from a live session, not copied from the documented example for
// the neighbouring event: there is no tool_output, and the command's output
// arrives in `error` behind an "Exit code N" line.
func hookInput(t *testing.T, tool, command, output string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUseFailure",
		"tool_name":       tool,
		"cwd":             t.TempDir(),
		"tool_input":      map[string]any{"command": command, "description": "Run it"},
		"tool_use_id":     "toolu_01VVcJcmoSsLpsSL1kQAFWjP",
		"error":           "Exit code 1\n" + output,
		"is_interrupt":    false,
		"duration_ms":     1120,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The line the agent prints in front of the output is the same sentence on
// every failure in every ecosystem. Carrying it into the query can only blur
// the search, and the rest of the string is the actual error.
func TestHookAsksAboutTheErrorNotTheExitCode(t *testing.T) {
	got := hookQuery("Exit code 1\nERR_MODULE_NOT_FOUND: cannot find module widget")
	if want := "ERR_MODULE_NOT_FOUND: cannot find module widget"; got != want {
		t.Errorf("hookQuery = %q, want %q", got, want)
	}
	// A failure that printed nothing leaves no question to ask.
	if got := hookQuery("Exit code 3"); got != "" {
		t.Errorf("hookQuery = %q, want empty", got)
	}
}

func hookHarness(t *testing.T, in string, mutate func(*hookEnv)) (*hookEnv, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	env := &hookEnv{
		stdin:  strings.NewReader(in),
		stdout: out,
		cfg:    func() (*config.Config, error) { return &config.Config{FailureHook: "on"}, nil },
		inspect: func(context.Context, string, [][]string) hookProject {
			return hookProject{Known: true, Stage: domain.StageProjectTest}
		},
		search: func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
			return domain.SearchResponse{Results: []domain.SearchResult{{
				Grade: domain.GradeExact, SampleID: "sha256:aaa",
			}}}, nil
		},
	}
	if mutate != nil {
		mutate(env)
	}
	return env, out
}

func hookContext(t *testing.T, out *bytes.Buffer) string {
	t.Helper()
	if strings.TrimSpace(out.String()) == "" {
		return ""
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("hook printed something the agent cannot parse: %q (%v)", out.String(), err)
	}
	if got.HookSpecificOutput.HookEventName != "PostToolUseFailure" {
		t.Errorf("hookEventName = %q", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

// The build just broke, which is the one moment this network exists for. The
// agent did not route it through run_observed_command — it ran the build in a
// shell, the way agents mostly do — so nothing asked on its behalf.
func TestHookAnswersAFailedBuildStep(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm test", "1 failing"), nil)
	if code := hookAgentMain(context.Background(), env); code != 0 {
		t.Fatalf("exit = %d, want 0 — a hook must never fail the session", code)
	}
	if ctx := hookContext(t, out); !strings.Contains(ctx, "sha256:aaa") {
		t.Errorf("additionalContext = %q, want the sample the network found", ctx)
	}
}

// One switch, and it is a config flag rather than an installer re-run: the
// agents offer no per-hook disable, and nobody who has to re-install to turn
// something back on ever turns it back on.
func TestHookSaysNothingWhenTurnedOff(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm test", "1 failing"), func(e *hookEnv) {
		e.cfg = func() (*config.Config, error) { return &config.Config{FailureHook: "off"}, nil }
	})
	var asked bool
	env.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
		asked = true
		return domain.SearchResponse{}, nil
	}
	hookAgentMain(context.Background(), env)
	if asked {
		t.Error("a hook that is off still searched")
	}
	if out.Len() != 0 {
		t.Errorf("a hook that is off still spoke: %q", out.String())
	}
}

// The scope is a failed BUILD step, not every command the agent runs. The test
// of what counts is the same classifier that decides which evidence stage a
// wrapped command records, so the hook speaks about exactly the commands the
// product already understands — one definition, not two.
func TestHookIgnoresCommandsTheProjectDoesNotRecognise(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "git status", "fatal"), func(e *hookEnv) {
		e.inspect = func(context.Context, string, [][]string) hookProject {
			return hookProject{Known: false}
		}
	})
	hookAgentMain(context.Background(), env)
	if out.Len() != 0 {
		t.Errorf("hook spoke about a command that is not a build step: %q", out.String())
	}
}

// A miss is silence. An agent that is interrupted to be told nothing was found
// learns to resent the interruption.
func TestHookIsSilentOnAMiss(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "npm test", "1 failing"), func(e *hookEnv) {
		e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
			return domain.SearchResponse{Miss: true}, nil
		}
	})
	hookAgentMain(context.Background(), env)
	if out.Len() != 0 {
		t.Errorf("hook spoke on a miss: %q", out.String())
	}
}

// Anything can arrive on that pipe. A hook that dies, hangs or prints garbage
// damages a session it was only ever meant to help.
func TestHookSurvivesJunkOnItsInput(t *testing.T) {
	for _, in := range []string{"", "not json", "{}", `{"tool_name":"Bash"}`} {
		env, out := hookHarness(t, in, nil)
		if code := hookAgentMain(context.Background(), env); code != 0 {
			t.Errorf("input %q: exit = %d, want 0", in, code)
		}
		if out.Len() != 0 {
			t.Errorf("input %q: hook spoke anyway: %q", in, out.String())
		}
	}
}

// The agent writes a shell line, not an argv. "cd api && npm test" is a build
// step with a chore in front of it, and reading only the first word would miss
// every one of them.
func TestHookReadsPastTheChoresInAShellLine(t *testing.T) {
	for _, c := range []struct {
		line string
		want [][]string
	}{
		// Operators are written against the word beside them, not spaced out.
		{"cd api && npm test | tee log.txt; echo done",
			[][]string{{"cd", "api"}, {"npm", "test"}, {"tee", "log.txt"}, {"echo", "done"}}},
		{"npm ci&&npm run build",
			[][]string{{"npm", "ci"}, {"npm", "run", "build"}}},
		// A separator inside quotes is text. Splitting there would invent a
		// command nobody ran and classify the project on it.
		{`go test ./... -run "A;B" && echo ok`,
			[][]string{{"go", "test", "./...", "-run", `"A;B"`}, {"echo", "ok"}}},
	} {
		if got := hookSegments(c.line); !reflect.DeepEqual(got, c.want) {
			t.Errorf("hookSegments(%q) =\n %q\nwant\n %q", c.line, got, c.want)
		}
	}
}
