//go:build windows

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/r2cuerdame/codesamplex/internal/winres"
)

func TestLauncherUnderAConsolelessMcpHostHoldsNoConsole(t *testing.T) {
	for _, c := range []struct {
		name     string
		manifest bool
	}{
		{"release manifest", true},
		{"no manifest", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			stable := launcherFor(t, c.manifest)
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(t.TempDir(), "host.txt")
			host := exec.Command(self, "-test.run=^TestLauncherConsolelessHostHelper$")
			host.Env = append(os.Environ(), "LAUNCHER_CONSOLELESS_HOST="+stable+"|"+report)
			host.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
			if out, err := host.CombinedOutput(); err != nil {
				t.Fatalf("host: %v %s", err, out)
			}
			raw, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("host wrote no report: %v", err)
			}
			var hostConsole, launcherConsole, payloadConsole bool
			var payloadWindow uintptr
			var echo string
			if _, err := fmt.Sscanf(string(raw), "hostConsole=%t launcherConsole=%t payloadConsole=%t payloadWindow=%d echo=%s",
				&hostConsole, &launcherConsole, &payloadConsole, &payloadWindow, &echo); err != nil {
				t.Fatalf("host report %q: %v", raw, err)
			}
			if hostConsole {
				t.Fatalf("the host owns a console; agy's shape was not reproduced: %s", raw)
			}
			if echo != "ping" {
				t.Fatalf("MCP stdio did not round-trip through the launcher: %s", raw)
			}
			if launcherConsole {
				t.Errorf("the launcher still holds a console while serving a consoleless host: %s", raw)
			}
			if payloadWindow != 0 {
				t.Errorf("the payload got console window %#x: %s", payloadWindow, raw)
			}
			if !payloadConsole {
				t.Errorf("the payload has no console at all, so every console program it starts would open one: %s", raw)
			}
		})
	}
}

func TestLauncherStartedForAPersonKeepsAConsole(t *testing.T) {
	for _, c := range []struct {
		name       string
		manifest   bool
		flags      uint32
		extraArgs  string
		wantWindow bool
	}{
		{"release manifest, new console", true, windows.CREATE_NEW_CONSOLE, "", true},
		{"no manifest, new console", false, windows.CREATE_NEW_CONSOLE, "", true},
		{"release manifest, detached", true, windows.DETACHED_PROCESS, "", false},
		{"release manifest, daemon without stdio", true, windows.CREATE_NEW_CONSOLE, "daemon run", false},
		{"release manifest, mcp without stdio", true, windows.CREATE_NEW_CONSOLE, "mcp", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			stable := launcherFor(t, c.manifest)
			report := filepath.Join(t.TempDir(), "person.txt")
			t.Setenv("LAUNCHER_PERSON_PROBE", report)
			args := "-test.run=^TestLauncherPersonProbeHelper$"
			if c.extraArgs != "" {
				args = args + " " + c.extraArgs
			}
			startWithoutStdio(t, stable, args, c.flags)
			raw, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("payload wrote no report: %v", err)
			}
			var window uintptr
			var stdoutConsole bool
			if _, err := fmt.Sscanf(string(raw), "payloadWindow=%d stdoutConsole=%t", &window, &stdoutConsole); err != nil {
				t.Fatalf("payload report %q: %v", raw, err)
			}
			if c.wantWindow && (window == 0 || !stdoutConsole) {
				t.Errorf("a launch meant for a person left the payload without a console window to write to: %s", raw)
			}
			if !c.wantWindow && window != 0 {
				t.Errorf("non-interactive or detached launch got console window %#x: %s", window, raw)
			}
		})
	}
}

func startWithoutStdio(t *testing.T, exe, args string, flags uint32) {
	t.Helper()
	cmdline, err := windows.UTF16PtrFromString(`"` + exe + `" ` + args)
	if err != nil {
		t.Fatal(err)
	}
	si := windows.StartupInfo{}
	si.Cb = uint32(unsafe.Sizeof(si))
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(nil, cmdline, nil, nil, false, flags, nil, nil, &si, &pi); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if ev, err := windows.WaitForSingleObject(pi.Process, 60_000); ev != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(pi.Process, 1)
		t.Fatalf("launcher did not finish: wait=%#x %v", ev, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil || code != 0 {
		t.Fatalf("launcher exit = %d %v", code, err)
	}
}

func TestLauncherPersonProbeHelper(t *testing.T) {
	report := os.Getenv("LAUNCHER_PERSON_PROBE")
	if report == "" {
		return
	}
	body := fmt.Sprintf("payloadWindow=%d stdoutConsole=%t", consoleWindow(), handleIsConsole(uintptr(syscall.Stdout)))
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

func launcherFor(t *testing.T, manifest bool) string {
	t.Helper()
	if !manifest {
		if left, _ := filepath.Glob("rsrc_windows_*.syso"); len(left) > 0 {
			t.Fatalf("%v would link the release manifest into a launcher this case needs without one; delete it", left)
		}
	}
	stable := installLauncher(t)
	if manifest {
		stampManifest(t, stable, winres.Manifest(winres.Binaries("v1.0.0")["cmd/csx-launcher"]))
	}
	return stable
}

func TestLauncherConsolelessHostHelper(t *testing.T) {
	spec := os.Getenv("LAUNCHER_CONSOLELESS_HOST")
	if spec == "" {
		return
	}
	stable, report, ok := strings.Cut(spec, "|")
	if !ok {
		os.Exit(3)
	}
	hostCP, _ := windows.GetConsoleCP()
	cmd := exec.Command(stable, "-test.run=^TestLauncherServeProbeHelper$")
	cmd.Env = append(os.Environ(), "LAUNCHER_SERVE_PROBE=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err1 := cmd.StdinPipe()
	stdout, err2 := cmd.StdoutPipe()
	write := func(body string) {
		if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	if err := errors.Join(err1, err2, cmd.Start()); err != nil {
		write(fmt.Sprintf("start: %v", err))
	}
	out := bufio.NewReader(stdout)
	ready, err := out.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		write(fmt.Sprintf("payload never reported: %v %q stderr=%q", err, ready, stderr.String()))
	}
	attached, _, _ := attachConsole.Call(uintptr(cmd.Process.Pid))
	if attached != 0 {
		_, _, _ = freeConsole.Call()
	}
	_, _ = io.WriteString(stdin, "ping\n")
	_ = stdin.Close()
	rest, _ := io.ReadAll(out)
	err = cmd.Wait()
	write(fmt.Sprintf("hostConsole=%t launcherConsole=%t %s %s\nexit=%v stderr=%q",
		hostCP != 0, attached != 0, strings.TrimSpace(ready), strings.TrimSpace(string(rest)), err, stderr.String()))
}

func TestLauncherServeProbeHelper(t *testing.T) {
	if os.Getenv("LAUNCHER_SERVE_PROBE") != "1" {
		return
	}
	cp, _ := windows.GetConsoleCP()
	fmt.Printf("payloadConsole=%t payloadWindow=%d\n", cp != 0, consoleWindow())
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Printf("echo=%s\n", strings.TrimSpace(line))
	os.Exit(0)
}

var attachConsole = syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole")

func stampManifest(t *testing.T, exe, manifest string) {
	t.Helper()
	k := windows.NewLazySystemDLL("kernel32.dll")
	name, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := k.NewProc("BeginUpdateResourceW").Call(uintptr(unsafe.Pointer(name)), 0)
	if h == 0 {
		t.Fatalf("BeginUpdateResource: %v", err)
	}
	data := []byte(manifest)
	if ok, _, err := k.NewProc("UpdateResourceW").Call(h, 24, 1, 0x0409, uintptr(unsafe.Pointer(&data[0])), uintptr(len(data))); ok == 0 {
		_, _, _ = k.NewProc("EndUpdateResourceW").Call(h, 1)
		t.Fatalf("UpdateResource: %v", err)
	}
	if ok, _, err := k.NewProc("EndUpdateResourceW").Call(h, 0); ok == 0 {
		t.Fatalf("EndUpdateResource: %v", err)
	}
}

func TestPipedStdioHidesTheChildEvenWithAConsole(t *testing.T) {
	if consoleIsTheOutput() {
		cmd := exec.Command("cmd", "/c", "echo hi")
		applyWindowPolicy(cmd)
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&createNoWindow != 0 {
			t.Error("a terminal session had its child's console taken away")
		}
		return
	}
	cmd := exec.Command("cmd", "/c", "echo hi")
	applyWindowPolicy(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("piped stdio still let the payload open a window")
	}
}

func TestTheWindowPolicyDistinguishesPipesFromEverythingElse(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if handleIsConsole(w.Fd()) {
		t.Error("a pipe was reported as a console")
	}
	if !handleIsPipe(w.Fd()) {
		t.Error("a pipe was not recognised as one")
	}

	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if handleIsPipe(null.Fd()) {
		t.Error("the null device was mistaken for a pipe")
	}

	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if handleIsPipe(f.Fd()) {
		t.Error("a file was mistaken for a pipe")
	}
}

func TestSubcommandClassificationForWindowPolicy(t *testing.T) {
	if !isMCPOrHelperCommand([]string{"mcp"}) || !isMCPOrHelperCommand([]string{"--debug", "mcp"}) || !isMCPOrHelperCommand([]string{"__purplepulse_send"}) {
		t.Error("expected mcp and helper subcommands to be classified as non-console commands")
	}
	if isMCPOrHelperCommand([]string{"search"}) || isMCPOrHelperCommand([]string{"daemon", "run"}) {
		t.Error("unexpected MCP/helper classification")
	}
	if !isNonInteractiveCommand([]string{"daemon", "run"}) || !isNonInteractiveCommand([]string{"worker", "start"}) || !isNonInteractiveCommand([]string{"mcp"}) {
		t.Error("expected daemon, worker, and mcp to be classified as non-interactive for console reclamation")
	}
	if isNonInteractiveCommand([]string{"init"}) || isNonInteractiveCommand([]string{"search"}) {
		t.Error("interactive CLI command was classified as non-interactive")
	}
}
