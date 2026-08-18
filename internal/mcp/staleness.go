package mcp

import (
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// processStart is captured before requests can be served.
var processStart = time.Now()

var stableExecutablePath, initialExecutableInfo = captureExecutable()

var updateNotice struct {
	sync.RWMutex
	text string
}

// SetUpdateNotice records that a verified replacement is on disk. The stdio
// process remains client-owned and deliberately never terminates itself.
func SetUpdateNotice(text string) {
	updateNotice.Lock()
	updateNotice.text = text
	updateNotice.Unlock()
}

// staleBuildNotice reports a replacement performed after this process began.
// It re-stats on every tool call: caching an empty answer would hide an update
// installed later in the same long-lived editor session.
func staleBuildNotice() string {
	updateNotice.RLock()
	n := updateNotice.text
	updateNotice.RUnlock()
	if n != "" {
		return n
	}
	if root := os.Getenv("CSX_LAUNCHER_ROOT"); root != "" {
		seq, _ := strconv.ParseUint(os.Getenv("CSX_ACTIVE_SEQUENCE"), 10, 64)
		return launcherStaleNotice(root, os.Getenv("CSX_PAYLOAD_VERSION"), seq, os.Getenv("CSX_ACTIVE_SHA256"))
	}
	if stableExecutablePath == "" {
		return ""
	}
	fi, err := os.Stat(stableExecutablePath)
	if err != nil {
		return ""
	}
	if initialExecutableInfo != nil && !os.SameFile(initialExecutableInfo, fi) {
		return staleMessage()
	}
	return staleAgainst(fi.ModTime(), processStart)
}

func launcherStaleNotice(root, version string, sequence uint64, sha string) string {
	a, err := launcher.Read(root)
	if err != nil {
		return staleMessage()
	}
	if a.Current.Version != version || a.Current.Sequence != sequence || a.Current.SHA256 != sha {
		return staleMessage()
	}
	return ""
}

func captureExecutable() (string, os.FileInfo) {
	path, err := os.Executable()
	if err != nil {
		return "", nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		return path, nil
	}
	return path, fi
}

func staleFileIdentity(path string, initial os.FileInfo) string {
	current, err := os.Stat(path)
	if err != nil || initial == nil || os.SameFile(initial, current) {
		return ""
	}
	return staleMessage()
}

func staleAgainst(binaryWritten, started time.Time) string {
	if !binaryWritten.After(started.Add(time.Second)) {
		return ""
	}
	return staleMessage()
}

func staleMessage() string {
	return "This csx MCP server is running a build that has since been replaced on disk. " +
		"It will keep answering with the old code until the editor restarts it — restart " +
		"your MCP client to pick up the new one."
}
