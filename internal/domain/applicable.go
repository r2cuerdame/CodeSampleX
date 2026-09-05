package domain

import (
	"sort"
	"strings"
)

// Some coordinates cannot hold some assets, and counting those as backlog is
// what made the backlog wrong.
//
// The authoring queue has always refused two shapes and the completeness
// census has always counted them as missing: measured on production, 398 npm
// per-platform native builds and one Gradle plugin marker sat inside the 1,372
// releases reported as "no sample", and the queue declined every one of them
// on every poll. And 507 releases were reported as "no dependency graph" in
// ecosystems where nothing can produce one. A scheduler built on that
// denominator emits work nobody can close.
//
// These live here because two places have to agree and did not: the queue
// re-derived the sample rules in Go on every request while the census used a
// different denominator entirely.

// npmPlatformTokens maps the platform names npm uses in package names to the
// OS a verifier reports. They are npm's own `os` values, which is why win32
// appears rather than windows.
var npmPlatformTokens = map[string]string{
	"darwin":  "darwin",
	"win32":   "windows",
	"linux":   "linux",
	"android": "android",
	"freebsd": "freebsd",
	"openbsd": "openbsd",
	"sunos":   "sunos",
	"aix":     "aix",
}

// npmArchTokens are the architecture names npm uses beside a platform. A
// platform token means a native build only when an architecture follows it:
// "linux-x64" is a binary, "linux-utils" is a library.
var npmArchTokens = map[string]bool{
	"x64": true, "x86": true, "ia32": true, "arm": true, "arm64": true,
	"aarch64": true, "64": true, "32": true, "ppc64": true, "ppc64le": true,
	"s390x": true, "riscv64": true, "mips64el": true, "loong64": true,
	"universal": true,
}

// gradlePluginMarkerSuffix is Gradle's own convention: the artifactId of a
// plugin marker is the plugin id with this appended, and such an artifact is
// always pom-only.
const gradlePluginMarkerSuffix = ".gradle.plugin"

// NPMPackagePlatform reads the platform out of a native-binary package name.
//
// npm publishes native code as one package per platform, named for the
// platform it holds: @tailwindcss/oxide-darwin-arm64 installs on macOS and
// nowhere else. What makes it unauthorable is not installability — the linux
// one installs perfectly on linux — but that the thing a sample would import
// is a .node binary the parent selects internally. Measured on the registry:
// @tailwindcss/oxide-linux-x64-gnu's main is tailwindcss-oxide.linux-x64-gnu
// .node, while its parent @tailwindcss/oxide is index.js. The parent is the
// package worth a sample and it does not match this pattern.
//
// The test is deliberately narrow, because refusing real work is worse than
// the problem it solves: a hyphen-delimited token that is exactly one of npm's
// platform names, FOLLOWED by an architecture. "windows-release" and
// "darwinia" are not platform builds.
func NPMPackagePlatform(name string) (string, bool) {
	// The scope names the publisher, not the platform: @esbuild/win32-x64 is
	// a platform build and @tailwindcss/oxide is not.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	parts := strings.Split(name, "-")
	for i := 0; i+1 < len(parts); i++ {
		os, ok := npmPlatformTokens[parts[i]]
		if ok && npmArchTokens[parts[i+1]] {
			return os, true
		}
	}
	return "", false
}

// MavenPomOnlyByName reports a Gradle plugin marker from its name alone.
//
// Gradle publishes one for every plugin id: a pom whose only job is to point
// at the artifact that does the work. No jar, no classes, no symbols, so no
// contract can call anything. One coordinate —
// org.jetbrains.kotlin.plugin.serialization.gradle.plugin — took an authoring
// slot on a 24-hour lease and held it: the agent tried 22 times, got as far as
// disassembling the csx binary looking for something to call, and every
// restart was handed the same coordinate. Sample production across the network
// fell from 33 an hour to nothing while that ran.
func MavenPomOnlyByName(name string) bool {
	artifact := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		artifact = name[i+1:]
	}
	return strings.HasSuffix(artifact, gradlePluginMarkerSuffix)
}

// SampleNotApplicable reports whether no sample can be written for this
// coordinate, and why. The reason is returned so a reader is told which rule
// closed it rather than seeing a coordinate quietly leave the backlog.
func SampleNotApplicable(ecosystem, name string) (string, bool) {
	switch ecosystem {
	case "npm":
		if _, locked := NPMPackagePlatform(name); locked {
			return "npm per-platform native build: what a sample would import is the .node binary its parent selects", true
		}
	case "maven":
		if MavenPomOnlyByName(name) {
			return "gradle plugin marker: a pom with no classes, so no contract can call anything", true
		}
	}
	return "", false
}

// dependencyScannable is the set of ecosystems whose adapter can read a
// resolved tree at all. An ecosystem outside it has no scanner in this
// binary, so a missing dependency graph there is unaskable rather than
// unmeasured — and the site says so on every release of every such package.
//
// It has to track scanner.EdgeScanner and it did not. goadapter grew one and
// this list did not follow, so every Go release on the public site said "no
// dependency scanner ships for golang" while the scanner was compiled into
// the binary serving the page. A hardcoded taxonomy beside a real capability
// is a claim that drifts silently, and this one drifted into telling visitors
// the opposite of the truth.
//
// The list stays here because internal/domain cannot import the adapters
// without inverting the layering. What stops it drifting again is
// TestTheDependencyTaxonomyMatchesTheRegisteredAdapters in adapters/, which
// derives the set from the adapters actually registered and fails in either
// direction: a scanner that ships and is not claimed, or a claim with no
// scanner behind it.
var dependencyScannable = map[string]bool{
	"npm": true, "pypi": true, "cargo": true, "golang": true,
}

// DependencyScannableEcosystems returns the ecosystems whose registered
// adapter can produce the dependency axis. Scheduler SQL receives this list
// as data instead of carrying a second hard-coded taxonomy that can drift
// from DependencyNotApplicable.
func DependencyScannableEcosystems() []string {
	out := make([]string, 0, len(dependencyScannable))
	for ecosystem := range dependencyScannable {
		out = append(out, ecosystem)
	}
	sort.Strings(out)
	return out
}

// DependencyNotApplicable reports whether no dependency graph can be produced
// for this ecosystem, and why.
//
// It is deliberately a fact about the SCANNER, not about the package: a golang
// module has dependencies, and saying otherwise would be the network asserting
// something it never measured. What this says is that nobody here can look.
func DependencyNotApplicable(ecosystem string) (string, bool) {
	if dependencyScannable[ecosystem] {
		return "", false
	}
	return "no dependency scanner ships for " + ecosystem + ": the tree is unread, not empty", true
}
