package update

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	// Use the installer's one server-pinned snapshot. Reading GitHub latest
	// here races publication and fails whenever the server has rolled back.
	raw, err := readBootstrapEnvelope(filepath.Join(rootAbs, "csx-manifest.new.json"))
	if err != nil {
		return launcher.Active{}, err
	}
	bootstrapRaw, err := readBootstrapEnvelope(filepath.Join(rootAbs, "csx-bootstrap.new.json"))
	if err != nil {
		return launcher.Active{}, err
	}
	m, err := VerifyBootstrapRelease(raw, bootstrapRaw, pub, time.Now().UTC(), currentVersion)
	if err != nil {
		return launcher.Active{}, err
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
	launcherPath := filepath.Join(rootAbs, "csx-launcher.new.exe")
	launcherInfo, err := os.Stat(launcherPath)
	if err != nil {
		return launcher.Active{}, err
	}
	launcherDigest, err := fileSHA256(launcherPath)
	if err != nil {
		return launcher.Active{}, err
	}
	if launcherInfo.Size() != a.LauncherSize || launcherDigest != a.LauncherSHA256 {
		return launcher.Active{}, errors.New("update: installer launcher does not match the signed manifest")
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
	// A server rollback may advertise an older, still valid signed snapshot.
	// That is installable on a clean machine, but must not downgrade an
	// already installed launcher or erase its observed sequence floor.
	if installed, readErr := launcher.Read(root); readErr == nil {
		cmp, cmpErr := CompareVersions(m.Version, installed.Current.Version)
		if cmpErr != nil || cmp < 0 || m.Sequence < installed.Current.Sequence {
			return launcher.Active{}, errors.New("update: installer refused to downgrade the installed release")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return launcher.Active{}, fmt.Errorf("update: existing launcher state is unreadable: %w", readErr)
	}
	// Signature/hash checks above must precede execution, and the self-test
	// must precede the durable active pointer change. A correctly signed but
	// unstartable launcher is still an unsuccessful installation.
	selfTestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(selfTestCtx, launcherPath, "--launcher-version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "csx-launcher "+launcher.ProtocolVersion {
		return launcher.Active{}, errors.New("update: signed installer launcher self-test failed")
	}
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

func readBootstrapEnvelope(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxManifestBytes {
		return nil, errors.New("update: bootstrap manifest envelope exceeds size limit")
	}
	return raw, nil
}
