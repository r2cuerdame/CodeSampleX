package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	csxupdate "github.com/r2cuerdame/codesamplex/internal/update"
)

// The signed manifest may not carry a field an already-shipped client
// rejects.
//
// v0.1.92 added launcherUrl/launcherSize/launcherSha256 so `csx update`
// could deliver the launcher. VerifyEnvelope decodes the signed payload with
// DisallowUnknownFields, so every client built before those fields existed
// stopped being able to read the manifest at all. Measured against the live
// manifest with a real v0.1.87 binary:
//
//	csx update: update: decode signed manifest: json: unknown field "launcherUrl"
//	exit 1
//
// An update is the only way to fix a client, and the update is what broke, so
// every csx at or below v0.1.91 was permanently frozen -- including the farm
// node, pinned at v0.1.90 and unable to move.
//
// The client half stays: a manifest that carries these fields is understood
// by v0.1.92 and later. The signer stops emitting them, because the fleet
// contains clients that cannot read them and there is no way to reach those
// clients except through the manifest they can no longer read.
//
// Re-enabling is not a date, it is a measurement: nothing may go back into
// this structure until no supported client rejects unknown fields.
func TestTheSignedManifestCarriesNothingAnOldClientRejects(t *testing.T) {
	dist := t.TempDir()
	const version = "v0.1.97"
	for _, name := range []string{
		"csx-darwin-amd64", "csx-darwin-arm64", "csx-linux-amd64", "csx-linux-arm64",
		"csx-windows-amd64.exe", "csx-windows-arm64.exe",
		"csx-launcher-windows-amd64.exe", "csx-launcher-windows-arm64.exe",
	} {
		if err := os.WriteFile(filepath.Join(dist, name), []byte("pretend "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, a := range assets(dist, version) {
		if err := csxupdate.ValidateLauncherAssetForTest(a, version); err != nil {
			t.Errorf("%s/%s: the verifier refuses what the signer wrote: %v", a.OS, a.Arch, err)
		}
		if a.LauncherURL != "" || a.LauncherSHA256 != "" || a.LauncherSize != 0 {
			t.Errorf("%s/%s carries launcher fields; every client below v0.1.92 "+
				"refuses the whole manifest and can never update again", a.OS, a.Arch)
		}
	}

	// And the emitted JSON must not name them either -- the field only has to
	// be PRESENT to break a strict decoder.
	raw, err := json.Marshal(assets(dist, version))
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"launcherUrl", "launcherSize", "launcherSha256"} {
		if strings.Contains(string(raw), field) {
			t.Errorf("the signed payload names %q", field)
		}
	}
}
