//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func isAntivirusIntervention(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, windows.ERROR_VIRUS_INFECTED) || errors.Is(err, windows.ERROR_VIRUS_DELETED) ||
		errors.Is(err, syscall.Errno(225)) || errors.Is(err, syscall.Errno(226)) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "virus") || strings.Contains(msg, "potentially unwanted software")
}

const jobObjectExtendedLimitInformation = 9
const jobObjectLimitKillOnJobClose = 0x00002000
const jobObjectLimitBreakawayOK = 0x00000800
const createNoWindow = 0x08000000
const fileTypePipe = 0x0003
const swHide = 0
const allocConsoleResultNewConsole = 1

type ioCounters struct{ ReadOperationCount, WriteOperationCount, OtherOperationCount, ReadTransferCount, WriteTransferCount, OtherTransferCount uint64 }
type basicLimitInformation struct {
	PerProcessUserTimeLimit, PerJobUserTimeLimit int64
	LimitFlags                                   uint32
	MinimumWorkingSetSize, MaximumWorkingSetSize uintptr
	ActiveProcessLimit                           uint32
	Affinity                                     uintptr
	PriorityClass, SchedulingClass               uint32
}
type extendedLimitInformation struct {
	BasicLimitInformation                                                        basicLimitInformation
	IoInfo                                                                       ioCounters
	ProcessMemoryLimit, JobMemoryLimit, PeakProcessMemoryUsed, PeakJobMemoryUsed uintptr
}

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var createJobObjectW = kernel32.NewProc("CreateJobObjectW")
var setInformationJobObject = kernel32.NewProc("SetInformationJobObject")
var assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
var getCurrentProcess = kernel32.NewProc("GetCurrentProcess")
var getConsoleCP = kernel32.NewProc("GetConsoleCP")
var getConsoleMode = kernel32.NewProc("GetConsoleMode")
var getFileType = kernel32.NewProc("GetFileType")
var closeHandle = kernel32.NewProc("CloseHandle")
var getConsoleWindow = kernel32.NewProc("GetConsoleWindow")
var getConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
var freeConsole = kernel32.NewProc("FreeConsole")
var allocConsoleWithOptions = kernel32.NewProc("AllocConsoleWithOptions")
var showWindow = syscall.NewLazyDLL("user32.dll").NewProc("ShowWindow")

var launcherJobOnce sync.Once
var launcherJobErr error
var launcherJob uintptr

func hideOwnConsoleWindow() {
	if !hasConsole() {
		reclaimDeclinedConsole()
		return
	}
	if !ownsItsConsoleAlone() {
		return
	}
	if !stdioUsesConsole() || isMCPOrHelperCommand(os.Args[1:]) || (handleIsPipe(uintptr(syscall.Stdin)) && handleIsPipe(uintptr(syscall.Stdout))) {
		_, _, _ = freeConsole.Call()
		return
	}
	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return
	}
	_, _, _ = showWindow.Call(hwnd, swHide)
}

func ownsItsConsoleAlone() bool {
	var pids [4]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}

func firstSubcommand(args []string) string {
	for _, arg := range args {
		if arg == "--debug" || arg == "--" || strings.HasPrefix(arg, "-test.") {
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

func isNonInteractiveCommand(args []string) bool {
	switch firstSubcommand(args) {
	case "mcp", "__purplepulse_send", "daemon", "worker":
		return true
	default:
		return false
	}
}

func reclaimDeclinedConsole() {
	if isNonInteractiveCommand(os.Args[1:]) || stdioUsable() || allocConsoleWithOptions.Find() != nil {
		return
	}
	var options struct {
		mode          int32
		useShowWindow int32
		showWindow    uint16
	}
	var result int32
	hr, _, _ := allocConsoleWithOptions.Call(uintptr(unsafe.Pointer(&options)), uintptr(unsafe.Pointer(&result)))
	if hr != 0 || result != allocConsoleResultNewConsole {
		return
	}
	for _, s := range []struct {
		id     int
		handle *syscall.Handle
		file   **os.File
		name   string
	}{
		{syscall.STD_INPUT_HANDLE, &syscall.Stdin, &os.Stdin, "/dev/stdin"},
		{syscall.STD_OUTPUT_HANDLE, &syscall.Stdout, &os.Stdout, "/dev/stdout"},
		{syscall.STD_ERROR_HANDLE, &syscall.Stderr, &os.Stderr, "/dev/stderr"},
	} {
		h, err := syscall.GetStdHandle(s.id)
		if err != nil {
			continue
		}
		*s.handle = h
		*s.file = os.NewFile(uintptr(h), s.name)
	}
}

func stdioUsable() bool {
	for _, h := range []syscall.Handle{syscall.Stdin, syscall.Stdout, syscall.Stderr} {
		if t, _, _ := getFileType.Call(uintptr(h)); t != 0 {
			return true
		}
	}
	return false
}

func stdioUsesConsole() bool {
	for _, h := range []syscall.Handle{syscall.Stdin, syscall.Stdout, syscall.Stderr} {
		if handleIsConsole(uintptr(h)) {
			return true
		}
	}
	return false
}

func hasConsole() bool {
	cp, _, _ := getConsoleCP.Call()
	return cp != 0
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

func consoleIsTheOutput() bool {
	if !hasConsole() {
		return false
	}
	if isMCPOrHelperCommand(os.Args[1:]) {
		return false
	}
	if handleIsPipe(uintptr(syscall.Stdin)) && handleIsPipe(uintptr(syscall.Stdout)) {
		return false
	}
	return true
}

func applyWindowPolicy(cmd *exec.Cmd) {
	if consoleIsTheOutput() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
	cmd.SysProcAttr.HideWindow = true
}

func runChild(cmd *exec.Cmd) (int, error) {
	if err := ensureLauncherJob(); err != nil {
		return 0, err
	}
	applyWindowPolicy(cmd)
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	if err != nil && cmd.Process == nil {
		return 0, &childStartError{err: err}
	}
	return 0, err
}

func ensureLauncherJob() error {
	launcherJobOnce.Do(func() {
		job, _, callErr := createJobObjectW.Call(0, 0)
		if job == 0 {
			launcherJobErr = fmt.Errorf("create launcher job: %w", callErr)
			return
		}
		info := extendedLimitInformation{}
		info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose | jobObjectLimitBreakawayOK
		if ok, _, err := setInformationJobObject.Call(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
			_, _, _ = closeHandle.Call(job)
			launcherJobErr = fmt.Errorf("configure launcher job: %w", err)
			return
		}
		current, _, _ := getCurrentProcess.Call()
		if ok, _, err := assignProcessToJobObject.Call(job, current); ok == 0 {
			_, _, _ = closeHandle.Call(job)
			launcherJobErr = fmt.Errorf("assign launcher to kill-on-close job: %w", err)
			return
		}
		launcherJob = job
	})
	return launcherJobErr
}
