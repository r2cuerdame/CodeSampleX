//go:build windows

package evidence

import (
	"os/exec"
	"syscall"
	"testing"
)

// A command observed from an MCP host must not open a window.
//
// Windows hands a console-subsystem child a brand new console — and a Terminal
// window on the desktop with it — whenever the process creating it owns none.
// An MCP host spawns csx over pipes and gives it no console, so every command
// run through run_observed_command flashed one: `npm test`, `go build`,
// whatever the tool was asked to observe.
//
// Standard handles are already redirected to the pipes this runner reads, so
// nothing about the output or the exit code changes.
func TestAnObservedCommandOpensNoWindowWithoutAConsole(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo hi")
	var tree processTree
	tree.prepare(cmd)

	if hasConsole() {
		// The test binary owns a console, which is the case this must NOT
		// touch: the child inherits it, no window appears, and taking the
		// console away could break a command that queries it.
		if cmd.SysProcAttr != nil && cmd.SysProcAttr.CreationFlags&createNoWindow != 0 {
			t.Error("the flag was set while this process owns a console")
		}
		return
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Error("a consoleless parent still lets its child open a window")
	}
}

// prepare must not disturb flags a caller already set.
func TestPrepareKeepsExistingCreationFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo hi")
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	cmd.SysProcAttr.CreationFlags |= 0x00000200 // CREATE_NEW_PROCESS_GROUP
	var tree processTree
	tree.prepare(cmd)
	if cmd.SysProcAttr.CreationFlags&0x00000200 == 0 {
		t.Error("prepare dropped a flag the caller had set")
	}
}
