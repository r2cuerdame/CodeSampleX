//go:build !windows

package purplepulse

import (
	"os/exec"
	"syscall"
)

func configureDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
