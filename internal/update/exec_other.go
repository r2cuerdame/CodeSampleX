//go:build !windows

package update

import "os/exec"

func prepareCommand(*exec.Cmd) {}
