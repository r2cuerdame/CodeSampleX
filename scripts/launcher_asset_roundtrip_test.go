package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// What the signer writes, the verifier must accept.
//
// The two live on opposite sides of a signature: assets() runs once in CI and
// VerifyEnvelope runs on every machine, every update. If they ever disagree
// about the shape of an asset, the manifest is signed successfully and then
// refused by every client -- an outage that starts at the moment of release
// and is invisible until someone runs an update.
func TestTheSignerProducesLauncherAssetsTheVerifierAccepts(t *testing.T) {
	dist := t.TempDir()
	const version = "v0.1.91"
	for _, name := range []string{
		"csx-darwin-amd64", "csx-darwin-arm64", "csx-linux-amd64", "csx-linux-arm64",
		"csx-windows-amd64.exe", "csx-windows-arm64.exe",
		"csx-launcher-windows-amd64.exe", "csx-launcher-windows-arm64.exe",
	} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte("pretend "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	out := assets(dist, version)
	windows := 0
	for _, a := range out {
		if err := csxupdate.ValidateLauncherAssetForTest(a, version); err != nil {
			t.Errorf("%s/%s: the verifier refuses what the signer wrote: %v", a.OS, a.Arch, err)
		}
		if !strings.EqualFold(a.OS, "windows") {
			if a.LauncherURL != "" || a.LauncherSHA256 != "" || a.LauncherSize != 0 {
				t.Errorf("%s/%s carries a launcher and has no launcher to carry", a.OS, a.Arch)
			}
			continue
		}
		windows++
		if a.LauncherSHA256 == "" || a.LauncherURL == "" || a.LauncherSize <= 0 {
			t.Errorf("%s/%s names no launcher, so csx update would leave it alone forever",
				a.OS, a.Arch)
		}
		if !strings.HasSuffix(a.LauncherURL, "csx-launcher-windows-"+a.Arch+".exe") {
			t.Errorf("%s/%s launcher URL is %q", a.OS, a.Arch, a.LauncherURL)
		}
	}
	if windows != 2 {
		t.Errorf("signed %d windows assets, want amd64 and arm64", windows)
	}
}
