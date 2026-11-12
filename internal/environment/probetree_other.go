//go:build !windows

package environment

import "os/exec"

// probeTree is a no-op off Windows: version probes there exec the tool
// directly, so the deadline's kill reaches the process that answers.
type probeTree struct{}

func (t *probeTree) prepare(*exec.Cmd) {}
func (t *probeTree) started(*exec.Cmd) {}
func (t *probeTree) release()          {}
