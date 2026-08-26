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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

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

// TestLauncherConsoleProbeHelper runs as the payload and reports the console
// window Windows gave it, which is the whole question behind R2C-103: a
// console-subsystem child gets a brand new console — and a visible Terminal
// window with it — when the process that created it owns none.
func TestLauncherConsoleProbeHelper(t *testing.T) {
	if os.Getenv("LAUNCHER_CONSOLE_PROBE") != "1" {
		return
	}
	hwnd := consoleWindow()
	visible := uintptr(0)
	if hwnd != 0 {
		visible, _, _ = isWindowVisible.Call(hwnd)
	}
	fmt.Printf("consoleWindow=%d visible=%d", hwnd, visible)
	os.Exit(0)
}

// TestLauncherLingerHelper runs as the payload, publishes its pid and then
// outlives the launcher unless something kills it.
func TestLauncherLingerHelper(t *testing.T) {
	marker := os.Getenv("LAUNCHER_TEST_LINGER")
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(3)
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

// TestLauncherInheritProbeHelper runs with a console of its own -- the shape a
// terminal gives `csx ...` -- and records both that console and the one the
// payload ends up in, so the caller can prove they are the same.
func TestLauncherInheritProbeHelper(t *testing.T) {
	spec := os.Getenv("LAUNCHER_INHERIT_PROBE")
	if spec == "" {
		return
	}
	stable, report, ok := strings.Cut(spec, "|")
	if !ok {
		os.Exit(3)
	}
	cmd := exec.Command(stable, "-test.run=^TestLauncherConsoleProbeHelper$")
	cmd.Env = append(os.Environ(), "LAUNCHER_CONSOLE_PROBE=1")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	payload, detail := int64(-1), ""
	if err := cmd.Run(); err != nil {
		detail = fmt.Sprintf("run launcher: %v stderr=%q", err, stderr.String())
	} else if _, err := fmt.Sscanf(stdout.String(), "consoleWindow=%d", &payload); err != nil {
		detail = fmt.Sprintf("payload said %q", stdout.String())
	}
	body := fmt.Sprintf("own=%d payload=%d\n%s", consoleWindow(), payload, detail)
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

var (
	user32Test         = syscall.NewLazyDLL("user32.dll")
	isWindowVisible    = user32Test.NewProc("IsWindowVisible")
	getConsoleWindowFn = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")
)

func consoleWindow() uintptr { h, _, _ := getConsoleWindowFn.Call(); return h }

// installLauncher builds the launcher into a fresh install root and pins this
// test binary as its active payload, so every launcher run re-enters one of the
// helpers above.
func installLauncher(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	stable := filepath.Join(root, "csx.exe")
	if out, err := exec.Command("go", "build", "-o", stable, ".").CombinedOutput(); err != nil {
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
	return stable
}

func TestLauncherForwardsStreamsArgumentsEnvironmentAndExit75(t *testing.T) {
	stable := installLauncher(t)
	cmd := exec.Command(stable, "-test.run=^TestLauncherPayloadHelper$", "--", "marker")
	cmd.Env = append(os.Environ(), "LAUNCHER_TEST_HELPER=1")
	cmd.Stdin = strings.NewReader("hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
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

// probe runs the console probe payload through the launcher and returns the
// window handle the payload reported.
func probe(t *testing.T, stable string, flags uint32) uintptr {
	t.Helper()
	cmd := exec.Command(stable, "-test.run=^TestLauncherConsoleProbeHelper$")
	cmd.Env = append(os.Environ(), "LAUNCHER_CONSOLE_PROBE=1")
	cmd.Stdin = strings.NewReader("")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if flags != 0 {
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("run launcher: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	var hwnd uintptr
	var visible int
	if _, err := fmt.Sscanf(stdout.String(), "consoleWindow=%d visible=%d", &hwnd, &visible); err != nil {
		t.Fatalf("probe output %q: %v", stdout.String(), err)
	}
	return hwnd
}

// An MCP host spawns csx over pipes with no console of its own, and so does the
// detached `csx daemon run`. The payload must not answer that by opening a
// Terminal window on the user's desktop.
func TestLauncherPayloadOpensNoConsoleWindowWhenLauncherHasNone(t *testing.T) {
	stable := installLauncher(t)
	if hwnd := probe(t, stable, windows.DETACHED_PROCESS); hwnd != 0 {
		t.Fatalf("payload allocated console window %#x; a consoleless launcher must not put one on screen", hwnd)
	}
}

// The counterpart contract: a person typing `csx ...` into a terminal must
// still get the payload inside that same console, not a private one. The test
// process itself usually has no console -- CI and MCP-hosted runs never do --
// so it stages the terminal by re-running itself with one.
func TestLauncherPayloadInheritsAnExistingConsole(t *testing.T) {
	stable := installLauncher(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "inherit.txt")
	cmd := exec.Command(self, "-test.run=^TestLauncherInheritProbeHelper$")
	cmd.Env = append(os.Environ(), "LAUNCHER_INHERIT_PROBE="+stable+"|"+report)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_CONSOLE}
	if err := cmd.Run(); err != nil {
		t.Fatalf("run console-owning probe: %v", err)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("probe wrote no report: %v", err)
	}
	var own, payload int64
	if _, err := fmt.Sscanf(string(raw), "own=%d payload=%d", &own, &payload); err != nil {
		t.Fatalf("report %q: %v", raw, err)
	}
	if own == 0 {
		t.Fatalf("probe never got a console of its own: %s", raw)
	}
	if payload != own {
		t.Fatalf("payload console window = %#x, want the launcher's %#x: %s", payload, own, raw)
	}
}

// The kill-on-close job is what keeps an MCP host's exit from leaving payloads
// behind; the console change must not cost the child that membership.
func TestLauncherJobKillsPayloadWhenLauncherDies(t *testing.T) {
	stable := installLauncher(t)
	marker := filepath.Join(t.TempDir(), "payload.pid")
	cmd := exec.Command(stable, "-test.run=^TestLauncherLingerHelper$")
	cmd.Env = append(os.Environ(), "LAUNCHER_TEST_LINGER="+marker)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	var pid int
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if raw, err := os.ReadFile(marker); err == nil {
			if pid, err = strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("payload never published its pid")
	}
	if !processAlive(pid) {
		t.Fatalf("payload %d already gone before the launcher was killed", pid)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("payload %d outlived the launcher", pid)
}

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == 259 // STILL_ACTIVE
}

// copyPayload installs this test binary as the payload for one version and
// returns the descriptor that addresses it.
func copyPayload(t *testing.T, root, version string, sequence uint64) launcher.Descriptor {
	t.Helper()
	testExe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(testExe)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := launcher.PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return launcher.Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
}

// installRoot builds the launcher into a fresh root without pinning a payload,
// so each test can stage the pointer state it needs.
func installRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", filepath.Join(root, "csx.exe"), ".").CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v: %s", err, out)
	}
	return root
}

func runLauncher(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(root, "csx.exe"), args...)
	cmd.Env = append(os.Environ(), "LAUNCHER_TEST_HELPER=1")
	cmd.Stdin = strings.NewReader("hello")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("run launcher: %v stderr=%q", err, stderr.String())
	}
	return exit.ExitCode(), stdout.String(), stderr.String()
}

// The failure this issue was filed for: an install whose current payload is
// gone. Whatever else happens, the caller must not be able to read that as the
// command it asked for having succeeded.
func TestLauncherExitsNonZeroWithAStableReasonWhenNoPayloadCanRun(t *testing.T) {
	root := installRoot(t)
	d := copyPayload(t, root, "v1.0.0", 1)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: d}); err != nil {
		t.Fatal(err)
	}
	payload, _ := launcher.PayloadPath(root, d.Version)
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runLauncher(t, root, "-test.run=^TestLauncherPayloadHelper$")
	if code == 0 {
		t.Fatalf("launcher reported success for an install it could not run: stdout=%q stderr=%q", stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("launcher wrote %q to stdout; an MCP host reads that as protocol framing", stdout)
	}
	if !strings.Contains(stderr, launcher.ReasonPayloadMissing) {
		t.Fatalf("stderr carries no stable reason: %q", stderr)
	}
}

// MCP stdio is where a silent failure does the most damage: an empty stdout and
// a clean exit is reported by the host as a server that closed, not one that
// could not start, and the session loses the tools without anyone being told.
func TestLauncherMcpStdioStartupFailureIsNotSilentSuccess(t *testing.T) {
	root := installRoot(t)
	d := copyPayload(t, root, "v1.0.0", 1)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: d}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(mustPayloadPath(t, root, d.Version))); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(filepath.Join(root, "csx.exe"), "mcp")
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() == 0 {
		t.Fatalf("mcp startup failure exited %v with stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("mcp startup failure wrote %q to the JSON-RPC stream", stdout.String())
	}
	if !strings.Contains(stderr.String(), launcher.ReasonPayloadMissing) {
		t.Fatalf("mcp startup failure carried no reason: %q", stderr.String())
	}
}

// The recovery, end to end: a current payload that vanished after it was
// verified must not stop csx while the pointer still records a payload that
// does verify. The run succeeds, the diagnostic says what happened, and the
// pointer is repaired so the next process -- and csx update -- sees it too.
func TestLauncherRunsLastKnownGoodWhenCurrentPayloadIsQuarantined(t *testing.T) {
	root := installRoot(t)
	previous := copyPayload(t, root, "v1.0.0", 1)
	current := copyPayload(t, root, "v1.1.0", 2)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(mustPayloadPath(t, root, current.Version)); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runLauncher(t, root, "-test.run=^TestLauncherPayloadHelper$", "--", "marker")
	if code != 75 {
		t.Fatalf("recovered run exited %d: stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "marker") || !strings.Contains(stdout, "stdin=hello") {
		t.Fatalf("recovered payload did not get the caller's arguments and stdin: %q", stdout)
	}
	if !strings.Contains(stderr, "recovered: "+launcher.ReasonPayloadMissing) || !strings.Contains(stderr, "v1.0.0") {
		t.Fatalf("recovery was not reported on stderr: %q", stderr)
	}

	a, err := launcher.Load(root)
	if err != nil {
		t.Fatalf("pointer left unloadable after recovery: %v", err)
	}
	if a.Current.Version != "v1.0.0" || a.RollbackHold == nil || a.RollbackHold.Version != "v1.1.0" {
		t.Fatalf("pointer not repaired: %+v", a)
	}

	rec, ok, err := launcher.ReadRecoveryRecord(root)
	if err != nil || !ok {
		t.Fatalf("no durable recovery evidence: %+v ok=%t err=%v", rec, ok, err)
	}
	if rec.FailedVersion != "v1.1.0" || rec.FailedReason != launcher.ReasonPayloadMissing || rec.RanVersion != "v1.0.0" {
		t.Fatalf("recovery record = %+v", rec)
	}
	if !rec.PointerRepaired {
		t.Fatalf("recovery record claims the pointer was not repaired: %+v", rec)
	}

	// A second run is an ordinary one: nothing left to recover from, and no
	// diagnostic to repeat.
	code, _, stderr = runLauncher(t, root, "-test.run=^TestLauncherPayloadHelper$")
	if code != 75 {
		t.Fatalf("run after repair exited %d: %q", code, stderr)
	}
	if strings.Contains(stderr, "recovered") {
		t.Fatalf("repaired install still reports a recovery: %q", stderr)
	}

	// And this is the whole point of the record. The install now looks and
	// behaves healthy; the one stderr line that said a released payload was
	// destroyed here has scrolled away and will never be printed again. What
	// an operator can still find has to survive that.
	after, ok, err := launcher.ReadRecoveryRecord(root)
	if err != nil || !ok {
		t.Fatalf("the repaired install lost its recovery evidence: %+v ok=%t err=%v", after, ok, err)
	}
	if after.Observations != rec.Observations {
		t.Fatalf("a healthy run counted itself as a recovery: %d then %d", rec.Observations, after.Observations)
	}
}

// Hash verification and CreateProcess are separate kernel operations. Defender
// or an ACL change can make the current executable unstartable in that window;
// the launcher must make one bounded attempt with the recorded LKG rather than
// treating a verified-but-unstartable current as the end of recovery.
func TestLauncherRunsLastKnownGoodWhenVerifiedCurrentCannotStart(t *testing.T) {
	root := installRoot(t)
	previous := copyPayload(t, root, "v1.0.0", 1)
	badBytes := []byte("this hashes correctly but is not a Windows executable")
	currentPath := mustPayloadPath(t, root, "v1.1.0")
	if err := os.MkdirAll(filepath.Dir(currentPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath, badBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(badBytes)
	current := launcher.Descriptor{Version: "v1.1.0", SHA256: hex.EncodeToString(sum[:]), Sequence: 2}
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runLauncher(t, root, "-test.run=^TestLauncherPayloadHelper$", "--", "start-fallback")
	if code != 75 {
		t.Fatalf("start-failure recovery exited %d: stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "start-fallback") || !strings.Contains(stdout, "stdin=hello") {
		t.Fatalf("fallback did not receive the original invocation: %q", stdout)
	}
	if !strings.Contains(stderr, "recovered: "+launcher.ReasonPayloadStartFailed) || !strings.Contains(stderr, previous.Version) {
		t.Fatalf("start-failure recovery was not diagnosed: %q", stderr)
	}
	a, err := launcher.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if a.Current != previous || a.RollbackHold == nil || *a.RollbackHold != current {
		t.Fatalf("start-failure recovery did not heal the pointer: %+v", a)
	}
}

func TestLauncherRetriesOnlyOnceWhenCurrentAndLastKnownGoodCannotStart(t *testing.T) {
	root := installRoot(t)
	writeInvalid := func(version, body string, sequence uint64) launcher.Descriptor {
		t.Helper()
		path := mustPayloadPath(t, root, version)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		raw := []byte(body)
		if err := os.WriteFile(path, raw, 0o700); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		return launcher.Descriptor{Version: version, SHA256: hex.EncodeToString(sum[:]), Sequence: sequence}
	}
	previous := writeInvalid("v1.0.0", "not executable previous", 1)
	current := writeInvalid("v1.1.0", "not executable current", 2)
	if err := launcher.Write(root, launcher.Active{Schema: 1, Current: current, Previous: &previous}); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runLauncher(t, root, "version")
	if code != 126 {
		t.Fatalf("two start failures exited %d: stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("failed retries wrote to MCP/CLI stdout: %q", stdout)
	}
	if got := strings.Count(stderr, "csx launcher: recovered:"); got != 1 {
		t.Fatalf("recovery attempts diagnosed %d times, want exactly one: %q", got, stderr)
	}
	if !strings.Contains(stderr, launcher.ReasonPayloadStartFailed) {
		t.Fatalf("final failure lost its stable reason: %q", stderr)
	}
}

func mustPayloadPath(t *testing.T, root, version string) string {
	t.Helper()
	path, err := launcher.PayloadPath(root, version)
	if err != nil {
		t.Fatal(err)
	}
	return path
}
