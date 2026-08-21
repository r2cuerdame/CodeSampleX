package cli

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

// Two agents, two ways of saying "that build failed".
//
// Claude Code raises a failure-only event and hands over the output in one
// string. Codex has no failure-only event at all: its PostToolUse fires after
// every command, successful ones included, and the payload it carries has no
// exit code anywhere in it — measured against Codex 0.149.0, not read from a
// document. A hook that spoke on every Codex PostToolUse would interrupt every
// command the agent ever got right.
//
// What Codex does give is transcript_path and tool_use_id, and the rollout
// file holds a CommandExecution item whose id is that tool_use_id, with the
// exit code on it. So the answer is recoverable — it just has to be fetched.
const (
	eventClaudeFailure = "PostToolUseFailure"
	eventPostToolUse   = "PostToolUse"
)

// hookFailureText reports the error a failed command printed, and whether the
// command failed at all.
//
// When it cannot establish that something failed it says so, and the caller
// stays quiet. Guessing "probably failed" is not available: on Codex that
// would interrupt every successful build.
func hookFailureText(p hookPayload) (string, bool) {
	switch p.HookEventName {
	case eventClaudeFailure:
		// This event only fires on failure, and `error` carries the output
		// behind an "Exit code N" line.
		text := hookQuery(p.Error)
		return text, text != ""

	case eventPostToolUse:
		exit, ok := codexExitCode(p.TranscriptPath, p.ToolUseID)
		if !ok || exit == 0 {
			return "", false
		}
		text := hookQuery(p.ToolResponse)
		return text, text != ""
	}
	return "", false
}

// codexExitCode finds what the command with this id exited with, by reading
// the rollout file Codex named in the event.
//
// The file is JSONL and the entry is written before the hook runs, but a
// missing or unflushed entry is a real possibility and is reported as "not
// known" rather than as a zero — the caller must be able to tell those apart.
func codexExitCode(transcriptPath, toolUseID string) (int, bool) {
	if strings.TrimSpace(transcriptPath) == "" || strings.TrimSpace(toolUseID) == "" {
		return 0, false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return 0, false
	}
	defer f.Close()

	var entry struct {
		Payload struct {
			Item struct {
				Type     string `json:"type"`
				ID       string `json:"id"`
				ExitCode *int   `json:"exit_code"`
			} `json:"item"`
		} `json:"payload"`
	}

	sc := bufio.NewScanner(f)
	// A rollout line carries the whole command and its output, which the
	// default 64KB scanner buffer will not hold for a real build.
	sc.Buffer(make([]byte, 0, 64*1024), codexTranscriptLineCap)
	code, found := 0, false
	for sc.Scan() {
		line := sc.Bytes()
		// The id is the cheapest possible filter and most lines lack it.
		if !strings.Contains(string(line), toolUseID) {
			continue
		}
		entry.Payload.Item.ExitCode = nil
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		it := entry.Payload.Item
		if it.Type != "CommandExecution" || it.ID != toolUseID || it.ExitCode == nil {
			continue
		}
		// Keep reading: the last completed entry for this id is the one that
		// says how it ended.
		code, found = *it.ExitCode, true
	}
	return code, found
}

// codexTranscriptLineCap bounds one rollout line. Build logs are large and a
// hook must not read an unbounded amount of somebody's session into memory.
const codexTranscriptLineCap = 4 << 20
