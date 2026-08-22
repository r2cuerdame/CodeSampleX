package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The updater downloads the next release to a temporary name next to the
// install — `.csx-update-*.exe` on Windows — and executes it before it is
// installed anywhere. That transient file has a different name and a
// different path on every single update, and on Windows an executable's
// path is its identity to the firewall: if this file ever opened a
// listening socket, the consent dialog would return on every update under
// a name the user has never seen, and the allow decision could never be
// remembered.
//
// R2C-84 was filed against exactly that shape, under the name
// `csx-server-new`. It turned out to come from a locally built csx-server
// rather than from here, but the invariant is worth holding: the one thing
// the updater is allowed to ask a staged binary to do is say its version.
const stagedSelfTestGuard = "CSX_UPDATE_STAGED_SELFTEST_VERSION"
const stagedSelfTestArgv = "CSX_UPDATE_STAGED_SELFTEST_ARGV"

// TestMain lets this test binary stand in for a freshly staged csx that the
// updater has just executed. The guard has to run before any test does, so
// the child's only output is the version line selfTestBinary parses.
func TestMain(m *testing.M) {
	if version := os.Getenv(stagedSelfTestGuard); version != "" {
		if path := os.Getenv(stagedSelfTestArgv); path != "" {
			_ = os.WriteFile(path, []byte(strings.Join(os.Args[1:], "\x00")), 0o600)
		}
		fmt.Println("csx " + version)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStagedBinaryIsOnlyEverExecutedToAskItsVersion(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, ".csx-update-1a2b3c")
	if runtime.GOOS == "windows" {
		staged += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, self, staged)

	argvFile := filepath.Join(dir, "argv")
	t.Setenv(stagedSelfTestGuard, "v9.9.9")
	t.Setenv(stagedSelfTestArgv, argvFile)

	if err := selfTestBinary(context.Background(), staged, "v9.9.9"); err != nil {
		t.Fatalf("selfTestBinary: %v", err)
	}

	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("the staged binary was never executed: %v", err)
	}
	got := strings.Split(string(raw), "\x00")
	if len(got) != 1 || got[0] != "version" {
		t.Fatalf("the updater ran the staged binary as %q; the only argument a not-yet-installed "+
			"binary may be given is \"version\", because anything that listens would raise the "+
			"Windows firewall dialog under this throwaway path", got)
	}
}

// A staged binary that reports the wrong version is rejected, and the
// rejection is the whole point of running it: an installer that skipped the
// self-test would promote a payload it never checked.
func TestStagedBinarySelfTestRejectsAMismatchedVersion(t *testing.T) {
	dir := t.TempDir()
	staged := filepath.Join(dir, ".csx-update-9z8y7x")
	if runtime.GOOS == "windows" {
		staged += ".exe"
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, self, staged)

	t.Setenv(stagedSelfTestGuard, "v1.0.0")
	t.Setenv(stagedSelfTestArgv, "")

	if err := selfTestBinary(context.Background(), staged, "v2.0.0"); err == nil {
		t.Fatal("selfTestBinary accepted a staged binary reporting a different version")
	}
}

func copyExecutable(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
