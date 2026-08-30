package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The Linux Python lane must be able to install what PyPI actually ships.
//
// It ran python:3.12-alpine. musl cannot use a manylinux wheel, so a project
// publishing manylinux only falls back to an sdist build that image has no
// toolchain for. Measured rather than assumed: llvmlite fails there and
// installs on the glibc lane, while numpy, lxml and pyarrow install on both —
// they publish musllinux wheels too. The break is narrower than "C
// extensions"; it is "no musllinux wheel", and llvmlite is the case nine
// defect reports named.
//
// The larger reason is the evidence itself. Every pypi receipt in production
// carries libc=musl, 510 of them and not one glibc, so a network built to
// answer "does it run there" had never once measured the environment nearly
// every Python user is in.
//
// The musl entries stay in the registry: 510 receipts name them, and
// PublishedImage has to keep resolving what already ran.
func TestTheLinuxPythonLaneCanInstallManylinuxWheels(t *testing.T) {
	for _, version := range []string{"", "3.12", "3.14"} {
		var m domain.SampleManifest
		m.Environment.Ecosystem = "pypi"
		m.Environment.RuntimeVersion = version

		img, err := imageForManifestLinux(m)
		if err != nil {
			t.Fatalf("python %q: %v", version, err)
		}
		entry, ok := registryEntryFor(img)
		if !ok {
			t.Fatalf("python %q: %s is not a registry entry", version, img)
		}
		if entry.libc != "glibc" {
			t.Errorf("python %q runs %s (libc=%s); a musl lane cannot install the wheels PyPI ships",
				version, entry.alias, entry.libc)
		}
		if strings.Contains(entry.alias, "alpine") {
			t.Errorf("python %q still selects %s", version, entry.alias)
		}
	}
}

// A receipt that named the musl image must still resolve, or 510 existing
// verifications stop being re-runnable against the bytes they ran on.
func TestTheMuslPythonImagesStayResolvable(t *testing.T) {
	for _, alias := range []string{"python:3.12-alpine", "python:3.14-alpine"} {
		entry, ok := verifierImages[alias]
		if !ok {
			t.Fatalf("%s was removed; receipts naming it can no longer be resolved", alias)
		}
		if _, published := PublishedImage(entry.ref()); !published {
			t.Errorf("%s no longer resolves as a published image", alias)
		}
	}
}
