package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func serverstoreRow(eco, name, version string) serverstore.WantedRow {
	return serverstore.WantedRow{
		Ecosystem: eco, Name: name, Version: version, Kind: "EXPANSION",
	}
}

// npm publishes native binaries as one package per platform, named for the
// platform they hold: @tailwindcss/oxide-darwin-arm64 installs on macOS and
// nowhere else. The queue handed those to a Linux worker — npm runs on Linux,
// so the ecosystem check passed — and the worker could not install what it
// was told to write a sample for.
func TestPlatformLockedPackagesAreRecognised(t *testing.T) {
	for name, want := range map[string]string{
		"@tailwindcss/oxide-darwin-arm64":  "darwin",
		"@tailwindcss/oxide-android-arm64": "android",
		"@esbuild/win32-x64":               "windows",
		"@rollup/rollup-linux-x64-gnu":     "linux",
		"@swc/core-darwin-x64":             "darwin",
		"esbuild-freebsd-64":               "freebsd",
	} {
		got, ok := npmPackagePlatform(name)
		if !ok || got != want {
			t.Errorf("%s -> %q ok=%v, want %q", name, got, ok, want)
		}
	}
}

// An ordinary package must not be mistaken for a platform build. "windows"
// inside a word is not a platform token, and refusing real work is worse than
// the problem this solves.
func TestOrdinaryPackagesAreNotPlatformLocked(t *testing.T) {
	for _, name := range []string{
		"axios", "@tailwindcss/oxide", "windows-release", "node-linux-utils",
		"react", "@types/node", "darwinia", "esbuild",
	} {
		if got, ok := npmPackagePlatform(name); ok {
			t.Errorf("%s was read as a %s build", name, got)
		}
	}
}

// A Linux worker cannot install a macOS binary, however well npm runs on
// Linux. The ecosystem check passed and the package could not, so the
// assignment was spent on work that could never finish.
func TestALinuxWorkerIsNotGivenAMacBinary(t *testing.T) {
	request := authoringWorkRequest{
		SandboxCapability: "CONTAINER_RUN", VerifierOS: []string{"linux"},
	}
	mac := serverstoreRow("npm", "@tailwindcss/oxide-darwin-arm64", "4.3.3")
	if authoringCandidateEligible(mac, request) {
		t.Error("a darwin binary was offered to a linux worker")
	}
	// Nor is its own platform, which this used to say was fair game.
	//
	// A linux worker installs @rollup/rollup-linux-x64-gnu perfectly and
	// still cannot write a sample for it: measured on the registry, its main
	// is ./rollup.linux-x64-gnu.node — the binary itself. So are
	// @tailwindcss/oxide-linux-x64-gnu and @napi-rs/lzma-linux-x64-gnu. There
	// is no JS API to call, on any platform, and the package worth a sample
	// is the parent that selects between these.
	linux := serverstoreRow("npm", "@rollup/rollup-linux-x64-gnu", "4.0.0")
	if authoringCandidateEligible(linux, request) {
		t.Error("a native binary package was offered as authoring work")
	}
	// And an ordinary package is untouched.
	plain := serverstoreRow("npm", "axios", "1.12.0")
	if !authoringCandidateEligible(plain, request) {
		t.Error("an ordinary package was refused")
	}
}
