//go:build windows

package environment

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// detectVirtualization reads the BIOS manufacturer strings the hypervisor
// writes. It is a cheap registry read — no WMI, no hostnames, no serials.
// Windows containers are rare enough that an undetected one honestly
// reports "" rather than a guess.
func detectVirtualization() (kind, runtime string) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\BIOS`, registry.QUERY_VALUE)
	if err != nil {
		return "", ""
	}
	defer k.Close()

	var joined string
	for _, name := range []string{"SystemManufacturer", "SystemProductName", "BIOSVendor"} {
		if v, _, err := k.GetStringValue(name); err == nil {
			joined += " " + strings.ToLower(v)
		}
	}
	for _, m := range []string{
		"vmware", "virtualbox", "innotek", "qemu", "kvm", "xen", "parallels",
		"microsoft corporation virtual", "hyper-v", "amazon ec2", "google",
	} {
		if strings.Contains(joined, m) {
			return "vm", ""
		}
	}
	return "", ""
}

// detectLibc is meaningless on Windows: there is one C runtime family and
// it never explains a cross-machine compatibility split the way musl vs
// glibc does on Linux.
func detectLibc() string { return "" }

// detectLibcVersion and detectDistro answer only on Linux; the glibc
// version and the /etc/os-release ID do not exist anywhere else, and an
// invented value would be a claim about a platform.
func detectLibcVersion() string { return "" }

func detectDistro() string { return "" }
