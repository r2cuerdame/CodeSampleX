package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

// BootstrapLauncher is the one-time Windows installer bridge. It verifies the
// signed stable manifest with the key embedded in the payload, then lets the
// shared launcher commit code durably promote that running staged payload.
func BootstrapLauncher(ctx context.Context, root, staged, legacy, currentVersion string) (launcher.Active, error) {
	if runtime.GOOS != "windows" {
		return launcher.Active{}, errors.New("update: launcher bootstrap is Windows-only")
	}
	local := os.Getenv("LOCALAPPDATA")
	wantRoot := filepath.Join(local, "csx")
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return launcher.Active{}, err
	}
	stagedAbs, err := filepath.Abs(staged)
	if err != nil {
		return launcher.Active{}, err
	}
	if local == "" || !strings.EqualFold(filepath.Clean(rootAbs), filepath.Clean(wantRoot)) || !strings.EqualFold(filepath.Clean(stagedAbs), filepath.Join(filepath.Clean(rootAbs), "csx-payload.new.exe")) {
		return launcher.Active{}, errors.New("update: launcher bootstrap paths are outside the first-party install root")
	}
	if legacy != "" && !strings.EqualFold(filepath.Clean(legacy), filepath.Join(filepath.Clean(rootAbs), "csx.exe")) {
		return launcher.Active{}, errors.New("update: legacy path is not the first-party stable path")
	}
	pub, err := EmbeddedPublicKey()
	if err != nil {
		return launcher.Active{}, err
	}
	c := &Client{}
	raw, err := c.get(ctx, DefaultManifestURL, maxManifestBytes)
	if err != nil {
		return launcher.Active{}, err
	}
	m, err := VerifyEnvelope(raw, pub, time.Now().UTC(), DefaultChannel)
	if err != nil {
		return launcher.Active{}, err
	}
	if m.Version != currentVersion {
		return launcher.Active{}, errors.New("update: installer payload does not match the signed stable release")
	}
	a, err := m.AssetFor("windows", runtime.GOARCH)
	if err != nil {
		return launcher.Active{}, err
	}
	fi, err := os.Stat(staged)
	if err != nil {
		return launcher.Active{}, err
	}
	digest, err := fileSHA256(staged)
	if err != nil {
		return launcher.Active{}, err
	}
	if fi.Size() != a.Size || digest != a.SHA256 {
		return launcher.Active{}, errors.New("update: installer payload does not match the signed manifest")
	}
	if a.MinLauncherVersion != "" {
		cmp, err := CompareVersions(launcher.ProtocolVersion, a.MinLauncherVersion)
		if err != nil || cmp < 0 {
			return launcher.Active{}, errors.New("update: installer launcher protocol is too old for this payload")
		}
	}
	unlock, err := acquireNamedLock(root+string(os.PathSeparator)+".update.lock", 30*time.Second)
	if err != nil {
		return launcher.Active{}, err
	}
	defer unlock()
	active, err := launcher.CommitPayload(root, staged, launcher.Descriptor{Version: m.Version, SHA256: a.SHA256, Sequence: m.Sequence})
	if err != nil {
		return launcher.Active{}, err
	}
	if legacy != "" && active.Previous == nil && m.Sequence > 1 {
		out, runErr := exec.CommandContext(ctx, legacy, "version").CombinedOutput()
		legacyVersion := strings.TrimPrefix(strings.TrimSpace(string(out)), "csx ")
		if runErr != nil || !IsCanonicalReleaseVersion(legacyVersion) {
			return launcher.Active{}, errors.New("update: legacy binary version self-test failed")
		}
		cmpLegacy, cmpErr := CompareVersions(legacyVersion, m.Version)
		if cmpErr != nil || cmpLegacy > 0 {
			return launcher.Active{}, errors.New("update: legacy binary is newer than installer payload")
		}
		if cmpLegacy == 0 {
			return active, nil
		}
		legacyHash, hashErr := fileSHA256(legacy)
		if hashErr != nil {
			return launcher.Active{}, hashErr
		}
		return launcher.ImportPrevious(root, legacy, launcher.Descriptor{Version: legacyVersion, SHA256: legacyHash, Sequence: m.Sequence - 1})
	}
	return active, nil
}
