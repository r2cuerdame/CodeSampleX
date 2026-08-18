//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

const jobObjectExtendedLimitInformation = 9
const jobObjectLimitKillOnJobClose = 0x00002000
const jobObjectLimitBreakawayOK = 0x00000800

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

func runChild(cmd *exec.Cmd) (int, error) {
	job, _, callErr := createJobObjectW.Call(0, 0)
	if job == 0 {
		return 0, fmt.Errorf("create launcher job: %w", callErr)
	}
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose | jobObjectLimitBreakawayOK
	if ok, _, err := setInformationJobObject.Call(job, jobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		return 0, fmt.Errorf("configure launcher job: %w", err)
	}
	current, _, _ := getCurrentProcess.Call()
	if ok, _, err := assignProcessToJobObject.Call(job, current); ok == 0 {
		return 0, fmt.Errorf("assign launcher to kill-on-close job: %w", err)
	}
	// The child inherits job membership at creation, eliminating the fast-child
	// Start->Assign race. Deliberately leave the handle open until process exit;
	// closing it here would also terminate this launcher, which is a job member.
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}
