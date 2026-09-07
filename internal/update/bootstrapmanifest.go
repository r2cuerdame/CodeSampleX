package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// VerifyBootstrapRelease binds the installer snapshot to its signed launcher
// descriptor. A separate envelope preserves the stable updater schema read by
// older clients, which reject launcher fields as unknown JSON fields.
func VerifyBootstrapRelease(stableRaw, bootstrapRaw []byte, pub ed25519.PublicKey, now time.Time, version string) (Manifest, error) {
	stable, err := VerifyEnvelope(stableRaw, pub, now, DefaultChannel)
	if err != nil {
		return Manifest{}, err
	}
	bootstrap, err := VerifyEnvelope(bootstrapRaw, pub, now, DefaultChannel)
	if err != nil {
		return Manifest{}, err
	}
	if stable.Version != version {
		return Manifest{}, errors.New("update: installer payload does not match the signed stable release")
	}
	withoutLaunchers := bootstrap
	withoutLaunchers.Assets = append([]Asset(nil), bootstrap.Assets...)
	for i, a := range withoutLaunchers.Assets {
		base := "https://github.com/r2cuerdame/CodeSampleX/releases/download/" + bootstrap.Version + "/"
		name := "csx-" + a.OS + "-" + a.Arch
		if a.OS == "windows" {
			name += ".exe"
		}
		if a.URL != base+name || (a.OS == "windows" && a.LauncherURL != base+"csx-launcher-windows-"+a.Arch+".exe") {
			return Manifest{}, errors.New("update: bootstrap assets must use immutable release URLs")
		}
		if a.OS == "windows" && a.LauncherURL == "" {
			return Manifest{}, errors.New("update: signed bootstrap manifest is missing a Windows launcher")
		}
		withoutLaunchers.Assets[i].LauncherURL = ""
		withoutLaunchers.Assets[i].LauncherSHA256 = ""
		withoutLaunchers.Assets[i].LauncherSize = 0
	}
	if !reflect.DeepEqual(stable, withoutLaunchers) {
		return Manifest{}, errors.New("update: bootstrap and stable manifests have different release identities")
	}
	return bootstrap, nil
}

// VerifyReleaseDirectory checks the complete payload and launcher set before
// publication or deployment makes its stable manifest visible to installers.
func VerifyReleaseDirectory(dist, version string, pub ed25519.PublicKey, now time.Time) error {
	stable, err := os.ReadFile(filepath.Join(dist, "csx-update-stable.json"))
	if err != nil {
		return err
	}
	bootstrap, err := os.ReadFile(filepath.Join(dist, "csx-bootstrap-stable.json"))
	if err != nil {
		return err
	}
	m, err := VerifyBootstrapRelease(stable, bootstrap, pub, now, version)
	if err != nil {
		return err
	}
	wantTargets := map[string]bool{"darwin/amd64": true, "darwin/arm64": true, "linux/amd64": true, "linux/arm64": true, "windows/amd64": true, "windows/arm64": true}
	if len(m.Assets) != len(wantTargets) {
		return errors.New("manifest release asset count mismatch")
	}
	check := func(url string, size int64, digest string) error {
		name := url[strings.LastIndex(url, "/")+1:]
		raw, err := os.ReadFile(filepath.Join(dist, name))
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		if int64(len(raw)) != size || hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("signed release asset mismatch: %s", name)
		}
		return nil
	}
	for _, a := range m.Assets {
		if !wantTargets[a.OS+"/"+a.Arch] {
			return errors.New("manifest release target set mismatch")
		}
		if err := check(a.URL, a.Size, a.SHA256); err != nil {
			return err
		}
		if a.OS == "windows" {
			if err := check(a.LauncherURL, a.LauncherSize, a.LauncherSHA256); err != nil {
				return err
			}
		}
	}
	return nil
}
