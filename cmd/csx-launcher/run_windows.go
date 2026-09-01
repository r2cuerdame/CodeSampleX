//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

const jobObjectExtendedLimitInformation = 9
const jobObjectLimitKillOnJobClose = 0x00002000
const jobObjectLimitBreakawayOK = 0x00000800
const createNoWindow = 0x08000000
const fileTypePipe = 0x0003

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

var launcherJobOnce sync.Once
var launcherJobErr error
var launcherJob uintptr

// hasConsole reports whether this launcher owns a console at all.
//
// Kept because it still answers half the question: a process attached to no
// console has nothing to hand the payload. It is no longer the whole test --
// see consoleIsTheOutput.
func hasConsole() bool {
	cp, _, _ := getConsoleCP.Call()
	return cp != 0
}

// handleIsConsole reports whether a handle is a console. GetConsoleMode
// succeeds only on a console handle.
func handleIsConsole(h uintptr) bool {
	var mode uint32
	r, _, _ := getConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return r != 0
}

// handleIsPipe reports whether a handle is a pipe, as opposed to a console, a
// file or the null device.
func handleIsPipe(h uintptr) bool {
	t, _, _ := getFileType.Call(h)
	return uint32(t) == fileTypePipe
}

// consoleIsTheOutput reports whether this launcher's own stdout is a console.
//
// Owning a console was the wrong question, and it answered wrongly for half
// the hosts. Measured on a workstation running four MCP clients at once:
//
//	conhost 28656 hosts csx.exe 32896   (parent claude.exe)
//	conhost 25852 hosts csx.exe  5252   (parent claude.exe)
//	conhost 38788 hosts csx.exe 32800   (parent claude.exe)
//	conhost 16644 hosts csx.exe 49156   (parent claude.exe)
//
// and no conhost at all for the csx processes another MCP host had spawned.
// One host allocates a console for the child and the other does not, so
// hasConsole() said yes for half of them, the flag stayed off, and the payload
// inherited a console nobody reads -- the window users report, titled with the
// payload path.
//
// What actually separates a person typing `csx search` from an MCP host is
// whether anything is reading the output. A terminal gives the process a
// console handle on stdout; an MCP host gives it a pipe, because that is what
// the protocol is. So the question is stdout, not ownership.
func consoleIsTheOutput() bool {
	if !hasConsole() {
		return false
	}
	// A pipe on both ends is what an MCP host looks like, and only that.
	//
	// The first attempt asked whether stdout was a console, which took the
	// console away from `csx search > out.txt` too -- a person in a terminal
	// redirecting output still wants Ctrl+C and still owns the console. A
	// pipe is the narrow signal: a terminal gives a console handle, a
	// redirect gives a file, the null device gives a character device, and
	// only a host speaking a protocol over stdio gives a pipe.
	if handleIsPipe(uintptr(syscall.Stdin)) && handleIsPipe(uintptr(syscall.Stdout)) {
		return false
	}
	return true
}

// applyWindowPolicy decides whether the child may keep a console window.
func applyWindowPolicy(cmd *exec.Cmd) {
	if consoleIsTheOutput() {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

func runChild(cmd *exec.Cmd) (int, error) {
	if err := ensureLauncherJob(); err != nil {
		return 0, err
	}
	// Windows gives a console-subsystem child a brand new console -- and a
	// Terminal window on the user's desktop with it -- whenever the process
	// creating it owns none. Every path where that is visible runs the launcher
	// consoleless: an MCP host spawning csx over pipes, and the DETACHED_PROCESS
	// spawn behind `csx daemon run`. There it is the payload, not the launcher,
	// that opens the window, which is why the title users report is the payload
	// path. CREATE_NO_WINDOW gives the child a console with no window; the std
	// handles are inherited explicitly through STARTUPINFO, so the stdio
	// contract, the exit code and job membership are all untouched. When the
	// launcher does own a console -- someone typing `csx search` -- the flag
	// stays off so the child inherits that console and keeps its output,
	// prompts and Ctrl+C exactly as before.
	applyWindowPolicy(cmd)
	// The child inherits job membership at creation, eliminating the fast-child
	// Start->Assign race. Deliberately leave the handle open until process exit;
	// closing it here would also terminate this launcher, which is a job member.
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	if err != nil && cmd.Process == nil {
		return 0, &childStartError{err: err}
	}
	return 0, err
}

// A launcher may make one bounded LKG retry after CreateProcess rejects the
// just-verified current payload. The launcher itself must join its kill-on-
// close job only once; trying to create and assign a second job on that retry
// is not portable across Windows job nesting policies.
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
		// Keep the raw handle alive for the process lifetime. Closing it is the
		// signal that terminates every payload inheriting this job.
		launcherJob = job
	})
	return launcherJobErr
}
