//go:build windows

package main

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

const fileTypePipe = 0x0003
const swHide = 0

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var getConsoleCP = kernel32.NewProc("GetConsoleCP")
var getConsoleMode = kernel32.NewProc("GetConsoleMode")
var getFileType = kernel32.NewProc("GetFileType")
var getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
var getConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
var freeConsole = kernel32.NewProc("FreeConsole")
var showWindow = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")

func hideOwnConsoleWindow() {
	if !hasConsole() || !ownsItsConsoleAlone() {
		return
	}
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	if !stdioUsesConsole() || isMCPOrHelperCommand(os.Args[1:]) || (handleIsPipe(uintptr(syscall.Stdin)) && handleIsPipe(uintptr(syscall.Stdout))) {
		_, _, _ = freeConsole.Call()
		return
	}
	_, _, _ = showWindow.Call(hwnd, swHide)
}

func hasConsole() bool {
	cp, _, _ := getConsoleCP.Call()
	return cp != 0
}

func ownsItsConsoleAlone() bool {
	var pids [4]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

func stdioUsesConsole() bool {
	for _, h := range []syscall.Handle{syscall.Stdin, syscall.Stdout, syscall.Stderr} {
		if handleIsConsole(uintptr(h)) {
			return true
		}
	}
	return false
}

func handleIsConsole(h uintptr) bool {
	var mode uint32
	r, _, _ := getConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

func handleIsPipe(h uintptr) bool {
	t, _, _ := getFileType.Call(h)
	return uint32(t) == fileTypePipe
}

func firstSubcommand(args []string) string {
	for _, arg := range args {
		if arg == "--debug" {
			continue
		}
		return strings.ToLower(strings.TrimSpace(arg))
	}
	return ""
}

func isMCPOrHelperCommand(args []string) bool {
	switch firstSubcommand(args) {
	case "mcp", "__purplepulse_send":
		return true
	default:
		return false
	}
}

func maybeSyncLauncherDetached(home, version string, args []string) {
	if firstSubcommand(args) == "update" {
		return
	}
	if os.Getenv("CSX_LAUNCHER_ROOT") == "" || !csxupdate.IsCanonicalReleaseVersion(version) {
		return
	}
	st, err := csxupdate.LoadState(home)
	if err != nil || st.LauncherVersion == version {
		return
	}
	if st.ConsecutiveFailures > 0 && time.Now().Before(st.NextCheck) {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	start := func(flags uint32) error {
		cmd := exec.Command(exe, "update", "apply")
		cmd.Env = append(os.Environ(), "CSX_HOME="+home)
		cmd.SysProcAttr = &syscall.SysProcAttr{
			CreationFlags: flags,
			HideWindow:    true,
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		_ = cmd.Process.Release()
		return nil
	}
	if err := start(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_BREAKAWAY_FROM_JOB); err != nil {
		_ = start(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	}
}
