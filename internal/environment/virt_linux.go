//go:build linux

package environment

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// detectVirtualization reports how the toolchain is isolated from real
// hardware. Everything it reads is a coarse population attribute — no
// hostnames, container IDs or machine IDs are touched.
func detectVirtualization() (kind, runtime string) {
	// WSL first: it is a VM, but its own population with its own quirks
	// (filesystem semantics, inotify, networking), so it gets its own name.
	if b, err := os.ReadFile("/proc/version"); err == nil {
		if strings.Contains(strings.ToLower(string(b)), "microsoft") {
			return "wsl", ""
		}
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "container", "docker"
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "container", "podman"
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(b)
		switch {
		case strings.Contains(s, "kubepods"):
			return "container", "kubernetes"
		case strings.Contains(s, "docker"):
			return "container", "docker"
		case strings.Contains(s, "containerd"):
			return "container", "containerd"
		case strings.Contains(s, "lxc"):
			return "container", "lxc"
		}
	}
	// DMI product name names the hypervisor on every common VM platform.
	if b, err := os.ReadFile("/sys/class/dmi/id/product_name"); err == nil {
		name := strings.ToLower(strings.TrimSpace(string(b)))
		for _, m := range []string{
			"virtualbox", "vmware", "kvm", "qemu", "hyper-v", "virtual machine",
			"xen", "amazon ec2", "google compute engine", "openstack", "parallels",
		} {
			if strings.Contains(name, m) {
				return "vm", ""
			}
		}
	}
	return "", ""
}

// detectLibc separates musl from glibc. Alpine images ship the musl
// loader at a well-known path; glibc systems ship ld-linux.
// detectLibcVersion reports the glibc version as "2.35", or "" when the
// libc is musl, unknown, or the probe cannot answer.
//
// The loader prints it: `ld-linux-x86-64.so.2 --version` opens with
// "ld.so (Ubuntu GLIBC 2.35-0ubuntu3.8) stable release version 2.35". No
// compiler and no package manager is involved, so it works in any
// container that has a glibc at all. getconf GNU_LIBC_VERSION is the
// fallback for images that ship it.
func detectLibcVersion() string {
	for _, loader := range []string{
		"/lib/ld-linux-x86-64.so.2",
		"/lib64/ld-linux-x86-64.so.2",
		"/lib/ld-linux-aarch64.so.1",
	} {
		if _, err := os.Stat(loader); err != nil {
			continue
		}
		out, err := exec.Command(loader, "--version").Output()
		if err != nil {
			continue
		}
		if v := glibcVersionFrom(string(out)); v != "" {
			return v
		}
	}
	if out, err := exec.Command("getconf", "GNU_LIBC_VERSION").Output(); err == nil {
		return glibcVersionFrom(string(out))
	}
	return ""
}

// glibcVersionFrom pulls the first major.minor after the word glibc.
func glibcVersionFrom(s string) string {
	m := reGlibcVersion.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return m[1]
}

var reGlibcVersion = regexp.MustCompile(`(?i)glibc[^0-9]{0,12}([0-9]+\.[0-9]+)`)

// detectDistro reads the /etc/os-release ID.
func detectDistro() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ID="); ok {
			return strings.ToLower(strings.Trim(strings.TrimSpace(v), `"`))
		}
	}
	return ""
}

func detectLibc() string {
	if m, _ := filepath.Glob("/lib/ld-musl-*"); len(m) > 0 {
		return "musl"
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		if strings.Contains(strings.ToLower(string(b)), "id=alpine") {
			return "musl"
		}
	}
	for _, p := range []string{"/lib/ld-linux-x86-64.so.2", "/lib64/ld-linux-x86-64.so.2", "/lib/ld-linux-aarch64.so.1"} {
		if _, err := os.Stat(p); err == nil {
			return "glibc"
		}
	}
	if m, _ := filepath.Glob("/lib/*/libc.so.6"); len(m) > 0 {
		return "glibc"
	}
	return ""
}
