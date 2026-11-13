//go:build windows

package environment

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// probeTree holds a version probe and everything it spawns. npm, pnpm and yarn
// are .cmd shims: the deadline kills cmd.exe while node, the process actually
// answering, keeps running with the caller's working directory (#689). A Job
// Object with KILL_ON_JOB_CLOSE takes the whole tree, as internal/evidence
// does for observed commands.
type probeTree struct {
	mu  sync.Mutex
	job windows.Handle
}

// prepare makes the context deadline terminate the job, not only the child.
func (t *probeTree) prepare(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		t.mu.Lock()
		if t.job != 0 {
			_ = windows.TerminateJobObject(t.job, 1)
		}
		t.mu.Unlock()
		return cmd.Process.Kill()
	}
}

// started puts the process in a job. A failure leaves the probe running as it
// did before: the deadline still kills the direct child.
func (t *probeTree) started(cmd *exec.Cmd) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	defer windows.CloseHandle(handle)
	if err := windows.AssignProcessToJobObject(job, handle); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	t.mu.Lock()
	t.job = job
	t.mu.Unlock()
}

// release closes the job; anything still inside dies with it.
func (t *probeTree) release() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}
