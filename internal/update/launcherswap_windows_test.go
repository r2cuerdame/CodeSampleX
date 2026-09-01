//go:build windows

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A launcher that will not start must never become the thing every csx
// invocation goes through, and a swap that fails must leave the machine with
// the launcher it already had.
func TestTheLauncherSwapRefusesABinaryThatWillNotStart(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := fileSHA256(exe)
	if err != nil {
		t.Fatal(err)
	}

	// Not a PE at all, so it cannot start.
	body := []byte("MZ this is not a program")
	sum := sha256.Sum256(body)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(body)),
		LauncherSHA256: hex.EncodeToString(sum[:]),
	})
	if replaced {
		t.Error("a launcher that cannot start was installed")
	}
	if err == nil {
		t.Error("the failed swap reported success")
	} else if !strings.Contains(err.Error(), "self-test") {
		t.Errorf("error names something other than the self-test: %v", err)
	}
	after, err := fileSHA256(exe)
	if err != nil {
		t.Fatalf("the installed launcher is gone: %v", err)
	}
	if after != before {
		t.Error("the installed launcher was changed by a swap that failed")
	}
	// And nothing was left lying beside it.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "csx.exe" {
			t.Errorf("the failed swap left %s behind", e.Name())
		}
	}
}

// Bytes that do not match the signed digest are refused before anything runs
// them, which is the whole reason the digest is in the SIGNED manifest.
func TestTheLauncherSwapRefusesBytesTheManifestDidNotSign(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("some other launcher entirely")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	_, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(body)),
		LauncherSHA256: strings.Repeat("a", 64),
	})
	if err == nil {
		t.Fatal("bytes that do not match the signed digest were accepted")
	}
	if !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("error names something other than the digest: %v", err)
	}
}

// The launcher already on disk is left alone, and nothing is downloaded.
func TestTheLauncherSwapIsANoOpWhenAlreadyCurrent(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	installed := []byte("the launcher already installed")
	if err := os.WriteFile(exe, installed, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(installed)

	asked := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = true
	}))
	defer srv.Close()

	c := &Client{HTTP: srv.Client()}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root, Asset{
		OS: "windows", Arch: "amd64",
		LauncherURL: srv.URL, LauncherSize: int64(len(installed)),
		LauncherSHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Error("an unchanged launcher was reported as replaced")
	}
	if asked {
		t.Error("the launcher was downloaded even though the installed one already matches")
	}
}

// A manifest with no launcher -- every one signed before the field existed --
// leaves the launcher alone rather than refusing the payload update.
func TestAManifestWithoutALauncherChangesNothing(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "csx.exe")
	if err := os.WriteFile(exe, []byte("the launcher already installed"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &Client{}
	replaced, err := c.replaceLauncherIfStale(context.Background(), root,
		Asset{OS: "windows", Arch: "amd64"})
	if err != nil || replaced {
		t.Errorf("replaced=%v err=%v, want a silent no-op", replaced, err)
	}
}
