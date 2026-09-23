package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrDoctorReleaseUnavailable = errors.New("signed release temporarily unavailable")

// VerifyInstalledStableRelease authenticates both signed release envelopes
// from the immutable release URL. It is shared by the CLI and the launcher so
// neither repair path can treat an unverified pointer hash as a signature.
func VerifyInstalledStableRelease(ctx context.Context, version string, client *http.Client) (Manifest, error) {
	if !IsCanonicalReleaseVersion(version) {
		return Manifest{}, errors.New("noncanonical release version")
	}
	pub, err := EmbeddedPublicKey()
	if err != nil {
		return Manifest{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	base := DefaultReleaseDownloadBase + "/" + version + "/"
	get := func(name string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDoctorReleaseUnavailable, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				return nil, fmt.Errorf("%w: HTTP %d", ErrDoctorReleaseUnavailable, resp.StatusCode)
			}
			return nil, fmt.Errorf("release HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	stable, err := get("csx-update-stable.json")
	if err != nil {
		return Manifest{}, err
	}
	bootstrap, err := get("csx-bootstrap-stable.json")
	if err != nil {
		return Manifest{}, err
	}
	return VerifyBootstrapRelease(stable, bootstrap, pub, time.Now().UTC(), version)
}

// RepairSignedLauncher refreshes only the launcher in a verified first-party
// install. The existing updater download, hash, self-test and atomic swap
// protocol performs the actual replacement.
func RepairSignedLauncher(ctx context.Context, root, version string) (bool, error) {
	local := os.Getenv("LOCALAPPDATA")
	if runtime.GOOS != "windows" || local == "" || !strings.EqualFold(filepath.Clean(root), filepath.Clean(filepath.Join(local, "csx"))) || SafeLauncherRepairTree(root, version) != nil {
		return false, errors.New("launcher is outside the CSX-owned install root")
	}
	m, err := VerifyInstalledStableRelease(ctx, version, nil)
	if err != nil {
		return false, err
	}
	asset, err := CurrentAsset(m)
	if err != nil {
		return false, err
	}
	if asset.OS != "windows" || asset.LauncherSHA256 == "" {
		return false, errors.New("signed Windows launcher asset is unavailable")
	}
	return (&Client{}).replaceLauncherIfStale(ctx, root, asset)
}

// RepairSignedStandalone replaces a damaged first-party Unix executable with
// the exact binary named by its signed release. Windows uses the launcher
// swap protocol instead of replacing a running standalone executable.
func RepairSignedStandalone(ctx context.Context, home, exe, version string) error {
	if runtime.GOOS == "windows" {
		return errors.New("Windows standalone replacement requires the official installer")
	}
	userHome, homeErr := os.UserHomeDir()
	if homeErr != nil || filepath.Clean(exe) != filepath.Join(userHome, ".local", "bin", "csx") {
		return errors.New("standalone executable is outside the standard CSX-owned install path")
	}
	if fi, err := os.Lstat(exe); err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("standalone executable is not a regular file")
	}
	owned, err := OwnsExecutable(home, exe)
	if err != nil || !owned {
		return errors.New("executable is not a CSX-owned standalone install")
	}
	in, err := LoadInstall(home)
	if err != nil || in.Kind != "standalone" {
		return errors.New("install is not CSX-owned standalone")
	}
	m, err := VerifyInstalledStableRelease(ctx, version, nil)
	if err != nil {
		return err
	}
	asset, err := CurrentAsset(m)
	if err != nil {
		return err
	}
	return WithLock(home, func() error {
		c := &Client{}
		staged, err := c.downloadAsset(ctx, exe, asset)
		if err != nil {
			return err
		}
		defer os.Remove(staged)
		if err := selfTestBinary(ctx, staged, version); err != nil {
			return err
		}
		if err := replaceExecutable(exe, staged, filepath.Join(home, "update", "previous-doctor")); err != nil {
			return err
		}
		got, err := fileSHA256(exe)
		if err != nil || got != asset.SHA256 {
			return errors.New("replaced executable failed re-verification")
		}
		return nil
	})
}

// SafeLauncherRepairTree refuses a junction or symlink in the install path
// before doctor writes payload bytes. Missing payload directories are allowed:
// restoring a quarantined payload is the main repair use case.
func SafeLauncherRepairTree(root, version string) error {
	if !IsCanonicalReleaseVersion(version) {
		return errors.New("noncanonical payload version")
	}
	for _, path := range []string{root, filepath.Join(root, "payloads"), filepath.Join(root, "payloads", version)} {
		fi, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && path != root {
			continue
		}
		if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("launcher repair path is not a regular CSX directory")
		}
	}
	return nil
}
