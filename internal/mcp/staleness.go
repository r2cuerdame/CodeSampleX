package mcp

import (
	"os"
	"sync"
	"time"
)

// processStart is when this server began. Captured at package init, before
// anything can spend time.
var processStart = time.Now()

var staleOnce struct {
	sync.Once
	notice string
}

// staleBuildNotice reports that this MCP server is running a build that has
// since been replaced on disk, or "" when it is current.
//
// An MCP server is a long-running process started by the editor, and
// upgrading csx replaces the binary underneath it. The running server keeps
// answering with the old code — the old grading, the old guards, the old
// bugs — until the editor is restarted, and nothing anywhere says so. A
// user who upgrades to get a fix can go days believing they have it.
//
// The check is a stat: if the executable on disk is newer than the moment
// this process started, the file was replaced after launch. That is exactly
// the condition, it needs no version registry, and it cannot produce a
// false positive from an ordinary upgrade the user has already restarted
// into, because such a process starts after the write.
//
// Computed once. A server that was current at startup does not become
// stale in a way worth re-statting on every call, and one that is stale
// stays stale until it is restarted.
func staleBuildNotice() string {
	staleOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		fi, err := os.Stat(exe)
		if err != nil {
			return
		}
		// A second of slack: the binary is written moments before the
		// process that runs it starts, and filesystem timestamps are not
		// always finer than the gap.
		staleOnce.notice = staleAgainst(fi.ModTime(), processStart)
	})
	return staleOnce.notice
}

// staleAgainst is the decision, separated so it can be tested without
// replacing a running binary.
func staleAgainst(binaryWritten, started time.Time) string {
	// A second of slack: the binary is written moments before the process
	// that runs it starts, and filesystem timestamps are not always finer
	// than that gap.
	if !binaryWritten.After(started.Add(time.Second)) {
		return ""
	}
	return "This csx MCP server is running a build that has since been replaced on disk. " +
		"It will keep answering with the old code until the editor restarts it — restart " +
		"your MCP client to pick up the new one."
}
