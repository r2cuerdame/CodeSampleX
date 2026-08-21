package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexTranscript writes the rollout file Codex hands the hook, holding one
// completed command execution. The shape was measured from Codex 0.149.0.
func codexTranscript(t *testing.T, id, output string, exit int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	status := "completed"
	if exit != 0 {
		status = "failed"
	}
	line, err := json.Marshal(map[string]any{
		"type": "event_msg",
		"payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{
				"type":              "CommandExecution",
				"id":                id,
				"status":            status,
				"exit_code":         exit,
				"aggregated_output": output,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session_meta"}` + "\n" + string(line) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Codex has no failure-only event and puts no exit code in the hook payload —
// both measured, not assumed. Its PostToolUse fires on success too, and the
// whole payload is tool_name, tool_input, tool_response (a plain string) and
// tool_use_id. A hook that spoke on every PostToolUse would interrupt every
// command the agent ever ran successfully.
//
// The exit code is recoverable: the rollout file named by transcript_path
// carries a CommandExecution item whose id is the hook's tool_use_id.
func TestCodexFailureIsFoundThroughTheTranscript(t *testing.T) {
	id := "exec-a4debc9f-25dc-4317-899e-8b14e4c86db7"
	out := "Error [ERR_REQUIRE_ESM]: require() of ES Module chalk not supported\n"

	text, failed := hookFailureText(hookPayload{
		HookEventName:  "PostToolUse",
		ToolName:       "Bash",
		ToolUseID:      id,
		TranscriptPath: codexTranscript(t, id, out, 1),
		ToolResponse:   out,
	})
	if !failed {
		t.Fatal("a command that exited 1 was not read as a failure")
	}
	if !strings.Contains(text, "ERR_REQUIRE_ESM") {
		t.Errorf("error text = %q, want the command's output", text)
	}
}

// The same event, and the command succeeded. Silence.
func TestCodexSaysNothingWhenTheCommandSucceeded(t *testing.T) {
	id := "exec-ok"
	_, failed := hookFailureText(hookPayload{
		HookEventName:  "PostToolUse",
		ToolName:       "Bash",
		ToolUseID:      id,
		TranscriptPath: codexTranscript(t, id, "all tests passed\n", 0),
		ToolResponse:   "all tests passed\n",
	})
	if failed {
		t.Error("a command that exited 0 was read as a failure")
	}
}

// The transcript may not have been flushed yet, or the id may not be in it.
// Guessing "probably failed" would interrupt successful commands, so an
// unanswerable question is answered with silence.
func TestCodexStaysSilentWhenTheExitCodeCannotBeFound(t *testing.T) {
	for _, c := range []struct{ name, id, path string }{
		{"id not in transcript", "exec-missing", codexTranscript(t, "exec-other", "x", 1)},
		{"no transcript at all", "exec-x", filepath.Join(t.TempDir(), "gone.jsonl")},
		{"no transcript path", "exec-x", ""},
	} {
		if _, failed := hookFailureText(hookPayload{
			HookEventName: "PostToolUse", ToolName: "Bash",
			ToolUseID: c.id, TranscriptPath: c.path, ToolResponse: "something",
		}); failed {
			t.Errorf("%s: guessed a failure it could not confirm", c.name)
		}
	}
}

// Claude Code's event needs no transcript: it fires only on failure and
// carries the output in `error`.
func TestClaudeFailureNeedsNoTranscript(t *testing.T) {
	text, failed := hookFailureText(hookPayload{
		HookEventName: "PostToolUseFailure",
		ToolName:      "Bash",
		Error:         "Exit code 1\nERR_MODULE_NOT_FOUND: cannot find module widget",
	})
	if !failed {
		t.Fatal("PostToolUseFailure was not read as a failure")
	}
	if text != "ERR_MODULE_NOT_FOUND: cannot find module widget" {
		t.Errorf("error text = %q", text)
	}
}
