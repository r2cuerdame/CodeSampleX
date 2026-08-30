package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The Linux Python lane must be able to install what PyPI actually ships.
//
// It ran python:3.12-alpine. Alpine is musl, musl rejects manylinux wheels,
// and manylinux is the format essentially every compiled PyPI package is
// distributed in — so pip fell back to building from source and any package
// with a C extension failed on a toolchain the image does not carry.
//
// This is not a theory. Eight separate defect reports arrived through
// report_csx_issue naming it, one of them with a worked example: llvmlite,
// where musl rejects the wheel and the sdist build finds no llvm-config. And
// the data agrees — every pypi receipt in production carries libc=musl, 510 of
// them, so a network built to answer "does it run there" had never once
// measured the environment nearly every Python user is in.
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
