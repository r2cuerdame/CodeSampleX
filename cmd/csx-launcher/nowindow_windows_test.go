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

// agy is the parent that was caught putting a Terminal window on a desktop
// every five minutes: it owns no console and starts `csx.exe mcp` over pipes
// with no creation flags, so Windows gave the launcher a console -- and, with
// Windows Terminal as the default terminal, a Terminal window -- before any of
// the launcher's code ran. This stages exactly that host and asks three
// things while the payload is serving: does the launcher still hold a
// console, did the payload get a window, and do the pipes still carry stdio.
//
// Both launcher shapes are covered. With the release manifest
// (consoleAllocationPolicy=detached) Windows 11 24H2 and later never allocate
// the console at all; internal/winres proves that on the loader. Without it --
// a Windows build that ignores the element, or a dev build with no resource
// object -- the console is allocated and the launcher must let go of it, which
// is the only thing left to do once it exists. Either way nothing may hold a
// console window for the length of the session, which is what SW_HIDE did
// under Windows Terminal: it hid a stub and the Terminal window stayed.
func TestLauncherUnderAConsolelessMcpHostHoldsNoConsole(t *testing.T) {
	for _, c := range []struct {
		name     string
		manifest bool
	}{
		{"release manifest", true},
		{"no manifest", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			stable := installLauncher(t)
			if c.manifest {
				stampManifest(t, stable, winres.Manifest(winres.Binaries("v1.0.0")["cmd/csx-launcher"]))
			}
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
			// CREATE_NO_WINDOW, not DETACHED_PROCESS: the payload has a console
			// with no window, which every build it runs inherits instead of
			// each allocating a window of its own.
			if !payloadConsole {
				t.Errorf("the payload has no console at all, so every console program it starts would open one: %s", raw)
			}
		})
	}
}

// TestLauncherConsolelessHostHelper is the host the test above stages: no
// console of its own (it is started DETACHED_PROCESS), csx over pipes, no
// creation flags. It does nothing in a normal run.
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
	// The payload is serving, so the launcher is past everything it does at
	// startup. AttachConsole succeeds only if it still holds a console.
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

// TestLauncherServeProbeHelper runs as the payload behind the staged host: it
// reports the console it was given, then serves one line of stdio.
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

// stampManifest writes manifest into exe as its application manifest --
// RT_MANIFEST, CREATEPROCESS_MANIFEST_RESOURCE_ID, en-US, as the release's
// rsrc object carries it -- so the launcher this package builds runs under the
// manifest the release links in.
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

// A launcher whose stdio is piped must not let the payload keep a console.
//
// Measured on this workstation while four MCP clients were running csx:
//
//	conhost 28656 hosts csx.exe 32896   (parent claude.exe)
//	conhost 25852 hosts csx.exe  5252   (parent claude.exe)
//	conhost 38788 hosts csx.exe 32800   (parent claude.exe)
//	conhost 16644 hosts csx.exe 49156   (parent claude.exe)
//
// and no conhost at all for the csx processes codex.exe had spawned. One MCP
// host allocates a console for the child and the other does not, so
// hasConsole() answered true for half of them and the payload inherited a
// console nobody reads — the window users report, titled with the payload
// path.
//
// Owning a console was the wrong question. What separates a person typing
// `csx search` from an MCP host is a pipe on both ends of stdio: a terminal
// gives console handles, a redirect to a file gives a file, the null device
// gives a character device, and only a host speaking a protocol over stdio
// gives pipes. Asking merely whether stdout was a console took the console
// away from `csx search > out.txt` as well, which still wants Ctrl+C.
func TestPipedStdioHidesTheChildEvenWithAConsole(t *testing.T) {
	if consoleIsTheOutput() {
		// `go test` from a terminal: the child must keep the console so its
		// output, prompts and Ctrl+C still work.
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

// The decision reads the KIND of handle, not whether a console exists.
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

	// The null device is a character device, not a pipe: redirecting to it is
	// not an MCP host, and a session doing that keeps its console.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if handleIsPipe(null.Fd()) {
		t.Error("the null device was mistaken for a pipe")
	}

	// So is a plain file.
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if handleIsPipe(f.Fd()) {
		t.Error("a file was mistaken for a pipe")
	}
}
