//go:build !windows

package sandbox

import "os/exec"

func prepareCommand(*exec.Cmd) {}
