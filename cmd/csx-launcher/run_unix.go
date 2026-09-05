//go:build !windows

package main

import (
	"os/exec"
	"strings"
)

func runChild(cmd *exec.Cmd) (int, error) {
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), nil
	}
	if err != nil && cmd.Process == nil {
		return 0, &childStartError{err: err}
	}
	return 0, err
}

// hideOwnConsoleWindow is a Windows concept; nothing here opens a window.
func hideOwnConsoleWindow() {}

// isAntivirusIntervention on Unix inspects error strings for security software intervention.
func isAntivirusIntervention(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "virus") || strings.Contains(msg, "potentially unwanted software")
}
