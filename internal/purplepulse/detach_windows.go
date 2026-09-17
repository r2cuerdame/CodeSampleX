//go:build windows

package purplepulse

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS | windows.CREATE_BREAKAWAY_FROM_JOB,
		HideWindow:    true,
	}
}
