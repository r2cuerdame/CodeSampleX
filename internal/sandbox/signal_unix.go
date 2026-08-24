//go:build !windows

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

func stageSignal(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if status, ok := ee.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return fmt.Sprint(status.Signal())
		}
	}
	return ""
}
