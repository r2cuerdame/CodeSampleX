//go:build !windows

package main

import (
	"os/exec"
)

func runChild(cmd *exec.Cmd) (int, error) {
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	return 0, err
}
