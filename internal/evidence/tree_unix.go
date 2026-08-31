//go:build !windows

package evidence

import (
	"os/exec"
	"syscall"
)

// processTree holds a command and everything it spawns, so a timeout can take
// the whole thing.
//
// exec.CommandContext kills only the process it started. Every command this
// network observes is a shell — `npm test`, `go test`, `sh -c` — so the thing
// doing the work is a grandchild, and on a timeout it survived: reported twice
// through report_csx_issue, once with a test child still running after the
// tool had returned. On a farm slot that is a run which already gave up still
// burning the CPU the next assignment needs.
//
// Setpgid makes the child its own group leader, and signalling the negative
// pid reaches every descendant that has not deliberately left the group.
type processTree struct{}

// prepare runs before Start and asks for the new process group.
func (t *processTree) prepare(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// started runs after Start. The group exists from creation here, so there is
// nothing to attach.
func (t *processTree) started(*exec.Cmd) {}

// kill signals the whole group: TERM first so a test runner can flush what it
// was writing, then KILL for anything that ignores it.
func (t *processTree) kill(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := -cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	_ = syscall.Kill(pgid, syscall.SIGKILL)
}

// release has nothing to hold: a process group is not a handle.
func (t *processTree) release() {}
