package search

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func linuxEnv(libc, ver string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "linux", Arch: "x64", Ecosystem: "npm",
		Runtime: "node", RuntimeVersion: "22.0.0",
		Libc: libc, LibcVersion: ver,
	}
}

// musl versus glibc is the first thing that stops a native module loading
// on Linux; the glibc VERSION is the second, and it was invisible. Both
// sides said "glibc", so a sample verified on Ubuntu 24.04 (2.39) was an
// EXACT libc match for a caller on CentOS 7 (2.17) — who gets
// "GLIBC_2.34 not found" at load time instead of an answer.
//
// Prebuilt wheels are named for this: manylinux2014 IS glibc 2.17.
func TestAnOlderGlibcIsNotAnExactMatch(t *testing.T) {
	req := linuxEnv("glibc", "2.17") // the caller, on an old distro
	sam := linuxEnv("glibc", "2.39") // the sample, verified on a new one

	var sawDifference bool
	for _, d := range compareEnv(req, sam, "npm", false) {
		if !d.equal && d.samShow != "" {
			sawDifference = true
		}
		if d.equal && d.exactEntry == "glibc" {
			t.Error("reported a bare glibc match across a version the binary cannot use")
		}
	}
	if !sawDifference {
		t.Error("no difference reported between glibc 2.17 and 2.39")
	}
}

// Compatibility runs one way: a NEWER glibc runs an older binary happily,
// so that direction must not be reported as a difference.
func TestANewerGlibcRunsAnOlderSample(t *testing.T) {
	req := linuxEnv("glibc", "2.39")
	sam := linuxEnv("glibc", "2.17")

	for _, d := range compareEnv(req, sam, "npm", false) {
		if !d.equal && d.samShow == "glibc 2.17" {
			t.Error("a newer caller glibc was reported as incompatible with an older sample")
		}
	}
}

// An unstated version is not a version that differs.
func TestAnUnknownGlibcVersionIsNotADifference(t *testing.T) {
	for _, pair := range [][2]string{{"", "2.39"}, {"2.17", ""}, {"", ""}} {
		req, sam := linuxEnv("glibc", pair[0]), linuxEnv("glibc", pair[1])
		for _, d := range compareEnv(req, sam, "npm", false) {
			if !d.equal && (d.samShow == sam.Libc || d.reqShow == req.Libc) {
				t.Errorf("versions %q/%q were treated as a libc difference", pair[0], pair[1])
			}
		}
	}
}

// The numbers are compared numerically: 2.9 is older than 2.35, which a
// string comparison gets backwards.
func TestGlibcVersionsCompareNumerically(t *testing.T) {
	if compareLibcVersions("2.9", "2.35") >= 0 {
		t.Error("2.9 did not sort below 2.35")
	}
	if compareLibcVersions("2.35", "2.35") != 0 {
		t.Error("equal versions did not compare equal")
	}
	if compareLibcVersions("2.40", "2.39") <= 0 {
		t.Error("2.40 did not sort above 2.39")
	}
}
