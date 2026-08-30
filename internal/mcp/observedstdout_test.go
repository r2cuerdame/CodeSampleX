package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/evidence"
)

// A command that printed on a successful run must have its output returned.
//
// It did not. structured.Stdout was filled only when the exit code was
// non-zero, so a passing command came back as:
//
//	{"exitCode":0,"stdout":"","stderr":"","stdoutTruncated":false}
//
// which does not read as "not provided" — it reads as "the command printed
// nothing", and the truncation flag adds a claim about a stream that was never
// looked at. Reported twice through report_csx_issue and reproduced here
// against production: `node -e "console.log('...')"` came back with an empty
// stdout and stdoutTruncated:false.
//
// It matters more over MCP than it looks. The runner tees the child's stdout
// to the server process's own stdout, which for a stdio MCP server is the
// protocol channel, not anything the agent reads. So on the success path the
// output was not merely unreported — for the caller it did not exist. An agent
// told to wrap its builds in this tool would get less than it gets from
// running the command itself, which is a reason to stop wrapping.
func TestASuccessfulCommandStillReportsWhatItPrinted(t *testing.T) {
	s := &Server{Deps: &Deps{RunObserved: func(context.Context, []string, string) (int, string, string, []string, evidence.CommandOutput, error) {
		return 0, "USED", "PASS", nil, evidence.CommandOutput{
			Stdout: "CSX_STDOUT_PROBE\n",
			Stderr: "a warning\n",
		}, nil
	}}}

	res := s.toolRunObserved(t.Context(), json.RawMessage(`{"command":["node","-e","x"]}`))
	if res == nil || res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		ExitCode        int    `json:"exitCode"`
		Stdout          string `json:"stdout"`
		Stderr          string `json:"stderr"`
		StdoutTruncated bool   `json:"stdoutTruncated"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.ExitCode != 0 {
		t.Fatalf("exitCode = %d", got.ExitCode)
	}
	if !strings.Contains(got.Stdout, "CSX_STDOUT_PROBE") {
		t.Errorf("stdout = %q; a passing command's output was dropped", got.Stdout)
	}
	if !strings.Contains(got.Stderr, "a warning") {
		t.Errorf("stderr = %q; a passing command's warnings were dropped", got.Stderr)
	}
}

// A truncation flag is a claim about a stream. It must not be emitted beside a
// stdout nobody filled.
func TestTruncationIsNotClaimedForAStreamThatWasNotReported(t *testing.T) {
	s := &Server{Deps: &Deps{RunObserved: func(context.Context, []string, string) (int, string, string, []string, evidence.CommandOutput, error) {
		return 0, "USED", "PASS", nil, evidence.CommandOutput{
			Stdout: "kept\n", StdoutTruncated: true,
		}, nil
	}}}
	res := s.toolRunObserved(t.Context(), json.RawMessage(`{"command":["x"]}`))
	raw, _ := json.Marshal(res.StructuredContent)
	var got struct {
		Stdout          string `json:"stdout"`
		StdoutTruncated bool   `json:"stdoutTruncated"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Stdout == "" {
		t.Fatal("stdout was dropped")
	}
	if !got.StdoutTruncated {
		t.Error("the stream was truncated and the flag says it was not")
	}
}
