//go:build linux

package environment

import (
	"os"
	"path/filepath"
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
