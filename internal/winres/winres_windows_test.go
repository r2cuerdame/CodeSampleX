//go:build windows

package winres

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The object has to survive the Go linker and come back out of the Win32
// version API, and the manifest has to be one the loader accepts: a malformed
// manifest fails process creation with a side-by-side error before main runs,
// which would break every install. So link the real object into a throwaway
// program, read it the way Explorer does, and run it.
func TestLinkedResourceReadsBackAndTheProgramStarts(t *testing.T) {
	goBin := toolchain(t)
	for _, pkg := range []string{"cmd/csx", "cmd/csx-launcher"} {
		info := Binaries("v0.1.105")[pkg]
		t.Run(info.FileDescription, func(t *testing.T) {
			exe := buildProbe(t, goBin, info)

			out, err := exec.Command(exe).CombinedOutput()
			if err != nil || string(out) != "started" {
				t.Fatalf("program with the manifest did not start: %v %q", err, out)
			}

			got := readVersionStrings(t, exe)
			for k, want := range map[string]string{
				"CompanyName":      "r2cuerdame",
				"ProductName":      "CodeSampleX",
				"FileDescription":  info.FileDescription,
				"OriginalFilename": info.OriginalFilename,
				"InternalName":     info.InternalName,
				"FileVersion":      "0.1.105",
				"ProductVersion":   "0.1.105",
				"LegalCopyright":   info.LegalCopyright,
			} {
				if got[k] != want {
					t.Errorf("%s = %q, want %q", k, got[k], want)
				}
			}
			if got["fixed"] != "0.1.105.0" {
				t.Errorf("fixed file version = %q, want 0.1.105.0", got["fixed"])
			}
		})
	}
}

// consoleAllocationPolicyBuild is the first Windows build that honours the
// manifest's consoleAllocationPolicy: Windows 11 24H2 and Windows Server 2025.
const consoleAllocationPolicyBuild = 26100

// The window an MCP host puts on the desktop for every csx is the console
// Windows allocates for a console-subsystem child of a parent that owns none.
// It exists before a line of the child's code runs, so nothing the launcher
// does at startup can take it back, and with Windows Terminal as the default
// terminal it is a Terminal window that ShowWindow cannot even reach.
//
// The launcher's manifest asks Windows not to allocate it. This proves it on
// the loader, three ways: a host with no console (agy, Claude Desktop, a
// spawned agent) gets a child with no console and pipes that still carry
// stdio; a host with a console -- a terminal -- still shares it with the
// child, so CLI output lands where it always did; and PowerShell 5.1 still
// captures the output, which is what the -H=windowsgui build lost. The
// payload's manifest is the control: same probe, same hosts, and Windows does
// allocate a console for it, so a pass is the manifest and not a host that
// happened to hand one down.
//
// A build older than 24H2 ignores the element. There the assertion flips: the
// launcher's probe still starts, still round-trips its pipes, and gets the
// console every console program gets -- the loader accepted the manifest.
func TestLauncherManifestAllocatesNoConsoleUnderAConsolelessHost(t *testing.T) {
	goBin := toolchain(t)
	build := windows.RtlGetVersion().BuildNumber
	honoured := build >= consoleAllocationPolicyBuild
	for _, c := range []struct {
		pkg      string
		detached bool
	}{
		{"cmd/csx-launcher", honoured},
		{"cmd/csx", false},
	} {
		info := Binaries("v0.1.105")[c.pkg]
		t.Run(info.FileDescription, func(t *testing.T) {
			exe := buildProbe(t, goBin, info)

			host := relay(t, exe, windows.DETACHED_PROCESS)
			if host.relayConsole {
				t.Fatalf("the relay owns a console; the consoleless host was not reproduced: %s", host.raw)
			}
			if host.echo != "ping" {
				t.Fatalf("stdio pipes did not round-trip under a consoleless host: %s", host.raw)
			}
			if host.console == c.detached {
				t.Errorf("console under a consoleless host = %t, want %t on build %d: %s", host.console, !c.detached, build, host.raw)
			}

			term := relay(t, exe, windows.CREATE_NEW_CONSOLE)
			if term.relayWindow == 0 {
				t.Fatalf("the terminal relay never got a console: %s", term.raw)
			}
			if !term.console || term.window != term.relayWindow || term.echo != "ping" {
				t.Errorf("a terminal's child did not share the terminal's console: %s", term.raw)
			}

			if c.pkg != "cmd/csx-launcher" {
				return
			}
			// `$v = csx version` is how install.ps1 checks itself. PowerShell
			// decides from the PE subsystem whether to redirect a native
			// command at all, so this is the regression a GUI build caused.
			ps := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command",
				"$v = 'ping' | & '"+exe+"' console; [Console]::Out.Write('captured=' + $v)")
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
			out, err := cmd.CombinedOutput()
			got := strings.TrimSpace(string(out))
			if err != nil || !strings.HasPrefix(got, "captured=console=true ") || !strings.HasSuffix(got, " echo=ping") {
				t.Errorf("PowerShell 5.1 did not capture the output: %v %q", err, got)
			}
		})
	}
}

// relayReport is what a staged host saw: its own console, and what the probe
// it spawned reported about its own.
type relayReport struct {
	relayConsole bool
	relayWindow  uintptr
	console      bool
	window       uintptr
	echo         string
	raw          string
}

// relay stages an MCP host: this test binary, started with flags that decide
// whether it owns a console, spawns the probe over pipes with no creation
// flags of its own -- exactly what a host does -- and writes down the result.
func relay(t *testing.T, probe string, flags uint32) relayReport {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(t.TempDir(), "relay.txt")
	cmd := exec.Command(self, "-test.run=^TestConsoleRelayHelper$")
	cmd.Env = append(os.Environ(), "WINRES_CONSOLE_RELAY="+probe+"|"+report)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("relay: %v %s", err, out)
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("relay wrote no report: %v", err)
	}
	r := relayReport{raw: string(raw)}
	head, probeOut, _ := strings.Cut(string(raw), "\n")
	if _, err := fmt.Sscanf(head, "relayConsole=%t relayWindow=%d", &r.relayConsole, &r.relayWindow); err != nil {
		t.Fatalf("relay report %q: %v", raw, err)
	}
	if _, err := fmt.Sscanf(probeOut, "console=%t window=%d", &r.console, &r.window); err != nil {
		t.Fatalf("probe output %q: %v", raw, err)
	}
	_, r.echo, _ = strings.Cut(probeOut, " echo=")
	return r
}

// TestConsoleRelayHelper is the host relay stages; it does nothing in a
// normal run.
func TestConsoleRelayHelper(t *testing.T) {
	spec := os.Getenv("WINRES_CONSOLE_RELAY")
	if spec == "" {
		return
	}
	probe, report, ok := strings.Cut(spec, "|")
	if !ok {
		os.Exit(3)
	}
	cp, _ := windows.GetConsoleCP()
	hwnd, _, _ := getConsoleWindow.Call()
	cmd := exec.Command(probe, "console")
	cmd.Stdin = strings.NewReader("ping\n")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	body := fmt.Sprintf("relayConsole=%t relayWindow=%d err=%v\n%s", cp != 0, hwnd, err, out.String())
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		os.Exit(4)
	}
	os.Exit(0)
}

var getConsoleWindow = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")

// probeMain is the program every resource object is linked into. With no
// arguments it only proves it started. With "console" it reports the console
// Windows gave it before any of its code ran, and echoes one line of stdin so
// the caller can see the stdio pipes still carry data.
const probeMain = `package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Print("started")
		return
	}
	k := syscall.NewLazyDLL("kernel32.dll")
	cp, _, _ := k.NewProc("GetConsoleCP").Call()
	hwnd, _, _ := k.NewProc("GetConsoleWindow").Call()
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	// PowerShell 5.1 puts a BOM in front of what it pipes in.
	line = strings.TrimSpace(strings.TrimPrefix(line, string(rune(0xfeff))))
	fmt.Printf("console=%t window=%d echo=%s", cp != 0, hwnd, line)
}
`

// toolchain returns the go binary that links the probes.
func toolchain(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds a program")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain on PATH")
	}
	return goBin
}

// buildProbe links info's real resource object into probeMain.
func buildProbe(t *testing.T, goBin string, info Info) string {
	t.Helper()
	dir := t.TempDir()
	obj, err := Syso(runtime.GOARCH, info)
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module probe\n\ngo 1.21\n")
	write("main.go", probeMain)
	write(SysoName(runtime.GOARCH), string(obj))

	exe := filepath.Join(dir, "probe.exe")
	build := exec.Command(goBin, "build", "-trimpath", "-o", exe, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off", "GOFLAGS=", "GOOS=windows", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("link with resource object: %v\n%s", err, out)
	}
	return exe
}

func readVersionStrings(t *testing.T, path string) map[string]string {
	t.Helper()
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		t.Fatalf("GetFileVersionInfoSize: %v (no version resource linked)", err)
	}
	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		t.Fatalf("GetFileVersionInfo: %v", err)
	}
	out := map[string]string{}
	var fixed *windows.VS_FIXEDFILEINFO
	var n uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\`, unsafe.Pointer(&fixed), &n); err != nil {
		t.Fatalf("VerQueryValue root: %v", err)
	}
	if fixed.Signature != 0xfeef04bd {
		t.Fatalf("fixed info signature = %#x", fixed.Signature)
	}
	out["fixed"] = strings.Join([]string{
		itoa(fixed.FileVersionMS >> 16), itoa(fixed.FileVersionMS & 0xffff),
		itoa(fixed.FileVersionLS >> 16), itoa(fixed.FileVersionLS & 0xffff),
	}, ".")
	for _, k := range []string{"CompanyName", "ProductName", "FileDescription", "OriginalFilename", "InternalName", "FileVersion", "ProductVersion", "LegalCopyright"} {
		var p *uint16
		if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\StringFileInfo\040904b0\`+k, unsafe.Pointer(&p), &n); err != nil {
			continue
		}
		out[k] = windows.UTF16PtrToString(p)
	}
	return out
}

func itoa(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
