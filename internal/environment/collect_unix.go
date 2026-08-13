//go:build !windows

package environment

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// osVersionBucket reports a coarse OS version: linux distro VERSION_ID major
// from /etc/os-release, or macOS product major. Unknown stays empty —
// a missing dimension is honest (goal.md §7.3).
func osVersionBucket() string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sw_vers", "-productVersion").Output()
		if err != nil {
			return ""
		}
		return strings.SplitN(strings.TrimSpace(string(out)), ".", 2)[0]
	}
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "VERSION_ID="); ok {
			v = strings.Trim(v, `"`)
			return strings.SplitN(v, ".", 2)[0]
		}
	}
	return ""
}
