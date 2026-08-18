//go:build windows

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

func TestLauncherPayloadHelper(t *testing.T) {
	if os.Getenv("LAUNCHER_TEST_HELPER") != "1" {
		return
	}
	raw, _ := io.ReadAll(os.Stdin)
	fmt.Printf("args=%s stdin=%s", strings.Join(os.Args[1:], ","), raw)
	fmt.Fprint(os.Stderr, "helper-stderr")
	os.Exit(75)
}

func TestLauncherForwardsStreamsArgumentsEnvironmentAndExit75(t *testing.T) {
	root := t.TempDir()
	stable := filepath.Join(root, "csx.exe")
	build := exec.Command("go", "build", "-o", stable, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v: %s", err, out)
	}
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := launcher.PayloadPath(root, "v1.0.0")
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(testExe)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	_ = in.Close()
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(payload)
	sum := sha256.Sum256(raw)
	d := launcher.Descriptor{Version: "v1.0.0", SHA256: hex.EncodeToString(sum[:]), Sequence: 1}
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: d}); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(stable, "-test.run=^TestLauncherPayloadHelper$", "--", "marker")
	cmd.Env = append(os.Environ(), "LAUNCHER_TEST_HELPER=1")
	cmd.Stdin = strings.NewReader("hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 75 {
		t.Fatalf("exit=%v stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "marker") || !strings.Contains(stdout.String(), "stdin=hello") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if stderr.String() != "helper-stderr" {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
