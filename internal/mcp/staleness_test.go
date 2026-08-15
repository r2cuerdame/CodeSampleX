package mcp

import (
	"os"
	"testing"
	"time"
)

// An MCP server is a long-running process started by the editor, and
// upgrading csx replaces the binary underneath it. The running server keeps
// answering with the old grading, the old guards and the old bugs until the
// editor restarts it, and nothing anywhere said so — so a user who upgraded
// to get a fix could go days believing they had it.
//
// Reproduced exactly this way tonight: the search fix landed, the binary
// was rebuilt, and the same question kept coming back with the old ranking
// because the MCP process predated the build.
func TestAReplacedBinaryIsReportedAsStale(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	fi, err := os.Stat(exe)
	if err != nil {
		t.Skip("cannot stat the test binary")
	}

	// Current: the binary predates this process, which is the normal case.
	if fresh := staleAgainst(fi.ModTime(), fi.ModTime().Add(time.Minute)); fresh != "" {
		t.Errorf("a current build was reported stale: %s", fresh)
	}
	// Replaced: the file on disk is newer than the process that runs it.
	if stale := staleAgainst(fi.ModTime().Add(time.Hour), fi.ModTime()); stale == "" {
		t.Error("a binary replaced after launch was not reported")
	}
	// A write in the same second as the launch is the ordinary
	// upgrade-then-start sequence, not a replacement.
	if edge := staleAgainst(fi.ModTime().Add(500*time.Millisecond), fi.ModTime()); edge != "" {
		t.Errorf("a same-second write was reported stale: %s", edge)
	}
}
