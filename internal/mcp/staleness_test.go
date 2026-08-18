package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func TestLauncherDescriptorDetectsSiblingPayloadActivation(t *testing.T) {
	root := t.TempDir()
	makePayload := func(version, body string, sequence uint64) launcher.Descriptor {
		path, err := launcher.PayloadPath(root, version)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body))
		return launcher.Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
	}
	old := makePayload("v1.0.0", "old", 1)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: old}); err != nil {
		t.Fatal(err)
	}
	if got := launcherStaleNotice(root, old.Version, old.Sequence, old.SHA256); got != "" {
		t.Fatalf("current payload stale: %s", got)
	}
	newPayload := makePayload("v1.1.0", "new", 2)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: newPayload, Previous: &old}); err != nil {
		t.Fatal(err)
	}
	if got := launcherStaleNotice(root, old.Version, old.Sequence, old.SHA256); got == "" {
		t.Fatal("old MCP missed active pointer flip")
	}
}

func TestAtomicReplacementUsesCapturedStablePathIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows launcher staleness uses active payload descriptors")
	}
	dir := t.TempDir()
	stable := filepath.Join(dir, "csx")
	previous := stable + ".previous"
	if err := os.WriteFile(stable, []byte("old"), 0o700); err != nil {
		t.Fatal(err)
	}
	initial, err := os.Stat(stable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stable, previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := staleFileIdentity(stable, initial); got == "" {
		t.Fatal("sibling MCP process missed replacement at captured stable path")
	}
}

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

func TestUpdateNoticeAppearsExactlyOnceOnEveryToolResult(t *testing.T) {
	SetUpdateNotice("restart required")
	defer SetUpdateNotice("")
	s := &Server{Deps: &Deps{LocalStats: func(context.Context) (map[string]any, error) { return map[string]any{"ok": true}, nil }}}
	params := json.RawMessage(`{"name":"get_local_stats","arguments":{}}`)
	got, rpcErr := s.toolsCall(context.Background(), params)
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	raw, _ := json.Marshal(got)
	if count := strings.Count(string(raw), "UPDATE NOTICE:"); count != 1 {
		t.Fatalf("notice count=%d result=%s", count, raw)
	}
}
