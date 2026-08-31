//go:build windows

package evidence

import (
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processTree holds a command and everything it spawns, so a timeout can take
// the whole thing.
//
// exec.CommandContext kills only the process it started. Every command this
// network observes is a shell — `npm test`, `powershell -File`, `cmd /c` — so
// the thing doing the work is a grandchild, and on a timeout it survived:
// reported twice through report_csx_issue, once with a PowerShell test child
// still running after the tool had returned. On a farm slot that is a run
// which already gave up still burning the CPU the next assignment needs.
//
// Windows has no process group to signal, so the tree is held by a Job Object.
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE terminates every member when the last
// handle closes, and a child that spawns its own children keeps them inside
// unless it deliberately breaks away.
type processTree struct {
	job windows.Handle
}

// prepare runs before Start. Nothing to set here: the job captures the process
// after it exists.
func (t *processTree) prepare(*exec.Cmd) {}

// started runs immediately after Start and puts the process in the job.
//
// There is a window between Start returning and this assignment in which a
// child could spawn outside the job. It is microseconds against the
// milliseconds a shell takes to load, and closing it properly needs
// CREATE_SUSPENDED plus the main thread handle, which os/exec does not expose.
// Naming the gap is better than a design that pretends to close it.
//
// Every failure here is silent and leaves the command running unsupervised —
// exactly as it ran before. A verification refusing to start because a job
// object could not be created would be worse than the leak it prevents.
func (t *processTree) started(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	t.job = job
}

// kill terminates every process still in the job.
func (t *processTree) kill(cmd *exec.Cmd) {
	if t.job == 0 {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return
	}
	_ = windows.TerminateJobObject(t.job, 1)
}

// release drops the job handle. KILL_ON_JOB_CLOSE means anything still inside
// dies with it, which is the backstop for a command that returned while a
// grandchild kept running.
func (t *processTree) release() {
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}
