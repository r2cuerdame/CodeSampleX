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
var closeHandle = kernel32.NewProc("CloseHandle")

var launcherJobOnce sync.Once
var launcherJobErr error
var launcherJob uintptr

// hasConsole reports whether this launcher owns a console the payload can be
// handed. GetConsoleCP has no console-independent meaning, so it fails --
// returning code page 0 -- for a process attached to none. A console with no
// window still answers, which is what keeps a host that already passed
// CREATE_NO_WINDOW down to a single console instead of two.
func hasConsole() bool {
	cp, _, _ := getConsoleCP.Call()
	return cp != 0
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
	if !hasConsole() {
		if cmd.SysProcAttr == nil {
			cmd.SysProcAttr = &syscall.SysProcAttr{}
		}
		cmd.SysProcAttr.CreationFlags |= createNoWindow
	}
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
