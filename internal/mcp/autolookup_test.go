package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func lookupServer(t *testing.T, mutate func(*Deps)) *Server {
	t.Helper()
	d := &Deps{}
	if mutate != nil {
		mutate(d)
	}
	return &Server{Deps: d}
}

func runArgsJSON(t *testing.T, argv ...string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(runArgs{Command: argv})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func resultText(out *toolResult) string {
	if out == nil || len(out.Content) == 0 {
		return ""
	}
	return out.Content[0].Text
}

// A build that just failed is the one moment the network exists for, and the
// product left calling it to the agent's discretion.
//
// An agent that hits a compile error has a fix it already believes in. Asking
// it to stop, remember a tool exists, and go look first is asking it to doubt
// itself at the exact moment it does not — so it does not ask. Six searches
// reached the server in a week while 648 misses and 143 adoptions did, and
// the searches that did happen were the ones somebody remembered to make.
//
// run_observed_command already wraps the build and already sanitizes the
// error. When the wrapped command fails, the lookup happens there, in the same
// turn, without anyone choosing to make it.
func TestAFailedBuildLooksItUpWithoutBeingAsked(t *testing.T) {
	var asked *domain.SearchRequest
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"ERR_MODULE_NOT_FOUND: cannot find <path>"}, commandOutput{}, nil
		}
		d.Search = func(_ context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			asked = &req
			return domain.SearchResponse{Results: []domain.SearchResult{{
				Grade: domain.GradeExact, SampleID: "sha256:aaa",
				Evidence: domain.EvidenceSummary{ContractPasses: 2},
			}}}, "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "build"))
	if asked == nil {
		t.Fatal("the build failed and nothing was looked up")
	}
	if !strings.Contains(resultText(out), "sha256:aaa") {
		t.Errorf("the answer never reached the agent:\n%s", resultText(out))
	}
}

// A build that passed asks nothing. There is no problem to solve, and a
// lookup on every green build is noise the agent has to read past.
func TestAPassingBuildLooksNothingUp(t *testing.T) {
	looked := false
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 0, "PROJECT_COMPILE", "PASS", nil, commandOutput{}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			looked = true
			return domain.SearchResponse{Miss: true}, ""
		}
	})
	s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "test"))
	if looked {
		t.Error("a green build triggered a lookup")
	}
}

// The exit code is the user's build result and must pass through untouched,
// whatever the lookup found. The wrapped command is the point; this is a
// passenger on it.
func TestTheLookupNeverChangesTheBuildResult(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 7, "PROJECT_TEST", "FAIL", []string{"ERR_ASSERTION"}, commandOutput{}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return domain.SearchResponse{Miss: true}, ""
		}
	})
	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "test"))
	structured := structured(t, out)
	if structured["exitCode"] != float64(7) {
		t.Errorf("exit code = %v, want the build's own 7", structured["exitCode"])
	}
	if out.IsError {
		t.Error("a failed build became a tool error")
	}
}

// A lookup that fails leaves the build report alone. The network being down
// is not the build's problem and must not read like one.
func TestALookupFailureIsSilent(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"ERR_X"}, commandOutput{}, nil
		}
		d.Search = nil
	})
	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "build"))
	if out.IsError {
		t.Error("a missing search path turned a build report into an error")
	}
	if !strings.Contains(resultText(out), "Exit code: 1") {
		t.Errorf("the build report was lost:\n%s", resultText(out))
	}
}
