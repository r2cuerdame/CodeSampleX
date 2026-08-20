package httpapi

import "strings"

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

// npmPackagePlatform reads the platform out of a native-binary package name.
//
// npm publishes native code as one package per platform, named for the
// platform it holds: @tailwindcss/oxide-darwin-arm64 installs on macOS and
// nowhere else. The authoring queue handed those to a Linux worker — npm runs
// on Linux, so the ecosystem check passed — and the worker could not install
// what it had been told to write a sample for.
//
// The name is the only signal available here: package.json's `os` field says
// it properly and never reaches this server. The convention is strong enough
// to act on — a hyphen-delimited token that is exactly one of npm's platform
// names — and the test is deliberately narrow, because refusing real work is
// worse than the problem it solves. "windows-release" and "darwinia" are not
// platform builds.
func npmPackagePlatform(name string) (string, bool) {
	// The scope names the publisher, not the platform: @esbuild/win32-x64 is
	// a platform build and @tailwindcss/oxide is not.
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	parts := strings.Split(name, "-")
	// The platform must be FOLLOWED by an architecture. That is what tells a
	// native build from a library that happens to mention a platform:
	// "rollup-linux-x64-gnu" is a binary, "node-linux-utils" is not, and both
	// carry the token in the same place.
	for i := 0; i+1 < len(parts); i++ {
		os, ok := npmPlatformTokens[parts[i]]
		if ok && npmArchTokens[parts[i+1]] {
			return os, true
		}
	}
	return "", false
}
