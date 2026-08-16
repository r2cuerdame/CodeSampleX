//go:build !linux && !windows

package environment

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// detectVirtualization on macOS and the BSDs asks sysctl for the
// hypervisor flag. Docker Desktop runs its containers inside a Linux VM,
// so code that actually runs in a container reports through the Linux
// path — a "container" verdict here would be wrong.
func detectVirtualization() (kind, runtime string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sysctl", "-n", "kern.hv_vmm_present").Output()
	if err == nil && strings.TrimSpace(string(out)) == "1" {
		return "vm", ""
	}
	return "", ""
}

// detectLibc: these platforms have one system C library, so the dimension
// carries no compatibility signal.
func detectLibc() string { return "" }

// detectLibcVersion and detectDistro answer only on Linux; the glibc
// version and the /etc/os-release ID do not exist anywhere else, and an
// invented value would be a claim about a platform.
func detectLibcVersion() string { return "" }

func detectDistro() string { return "" }
