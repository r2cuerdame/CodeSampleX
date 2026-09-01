package update

import (
	"strings"
	"testing"
)

// The signed manifest has to name the launcher, not only the payload.
//
// `csx update` replaces the payload and nothing else. The only code that ever
// downloads a launcher lives in install.ps1, so every launcher-side fix --
// the quarantine rehydrate from #67, the console-subsystem build -- reaches a
// machine only if its owner re-runs the installer. Measured on a workstation
// running v0.1.89: the payload was current and the launcher was the one
// installed 26 releases earlier, missing both.
//
// It has to be the SIGNED manifest and not SHA256SUMS.txt. That file is
// served beside the release and nothing vouches for it; the manifest is the
// one artifact the updater's own key covers, and a launcher is a binary that
// runs before the payload does. Anything less would let whoever can write the
// download surface choose the first thing that executes.
func TestTheSignedManifestCarriesTheWindowsLauncher(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asset Asset
		ok    bool
	}{
		{"a windows asset with its launcher", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 17203200,
			LauncherURL: "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/" +
				"csx-launcher-windows-amd64.exe",
			LauncherSHA256: strings.Repeat("b", 64), LauncherSize: 6897152,
		}, true},
		// Absent is legal: manifests signed before this field existed are
		// still valid, and a client that finds no launcher simply leaves the
		// one on disk alone rather than refusing the payload update.
		{"a windows asset from an older signer", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.87/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 17201664,
		}, true},
		// Half a launcher is not a launcher. A URL with no digest would be a
		// binary nothing holds to anything.
		{"a launcher URL with no digest", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 1,
			LauncherURL: "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/" +
				"csx-launcher-windows-amd64.exe",
		}, false},
		{"a launcher digest with no URL", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 1,
			LauncherSHA256: strings.Repeat("b", 64),
		}, false},
		{"a launcher digest that is not a digest", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 1,
			LauncherURL: "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/" +
				"csx-launcher-windows-amd64.exe",
			LauncherSHA256: "not-a-digest", LauncherSize: 1,
		}, false},
		// And the URL has to name the launcher for this exact release, by the
		// same rule the payload URL is held to.
		{"a launcher URL pointing somewhere else", Asset{
			OS: "windows", Arch: "amd64",
			URL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-windows-amd64.exe",
			SHA256: strings.Repeat("a", 64), Size: 1,
			LauncherURL:    "https://example.com/csx-launcher-windows-amd64.exe",
			LauncherSHA256: strings.Repeat("b", 64), LauncherSize: 1,
		}, false},
	} {
		err := validateLauncherAsset(tc.asset, "v0.1.90")
		if tc.ok && err != nil {
			t.Errorf("%s: rejected: %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: accepted, want a refusal", tc.name)
		}
	}
}

// A non-Windows asset never carries one, because there is no launcher there.
func TestOnlyWindowsAssetsMayNameALauncher(t *testing.T) {
	err := validateLauncherAsset(Asset{
		OS: "linux", Arch: "amd64",
		URL:            "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-linux-amd64",
		SHA256:         strings.Repeat("a", 64),
		LauncherURL:    "https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.90/csx-launcher-windows-amd64.exe",
		LauncherSHA256: strings.Repeat("b", 64), LauncherSize: 1,
	}, "v0.1.90")
	if err == nil {
		t.Error("a linux asset was allowed to name a launcher")
	}
}
