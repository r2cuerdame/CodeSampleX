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

	"github.com/r2cuerdame/codesamplex/internal/launcher"
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

func launcherRootOwned(root, local string) bool {
	wantRoot := filepath.Join(local, "csx")
	r1, err1 := resolveExistingPath(root)
	r2, err2 := resolveExistingPath(wantRoot)
	if err1 == nil && err2 == nil {
		return strings.EqualFold(r1, r2)
	}
	return strings.EqualFold(filepath.Clean(root), filepath.Clean(wantRoot))
}

// RepairSignedLauncher refreshes only the launcher in a verified first-party
// install. The existing updater download, hash, self-test and atomic swap
// protocol performs the actual replacement.
func RepairSignedLauncher(ctx context.Context, root, version string) (bool, error) {
	local := os.Getenv("LOCALAPPDATA")
	if runtime.GOOS != "windows" || local == "" || !launcherRootOwned(root, local) || SafeLauncherRepairTree(root, version) != nil {
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
	paths := []string{root, filepath.Join(root, "payloads"), filepath.Join(root, "payloads", version)}
	if active, err := launcher.Read(root); err == nil && active.Previous != nil {
		paths = append(paths, filepath.Join(root, "payloads", active.Previous.Version))
	}
	for _, path := range paths {
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

// RepairReleaseBinding downloads the payload for a signed stable release,
// checks its signature and digest through the rehydrate protocol, places it at
// its immutable path, reconciles active.json, and re-verifies.
func RepairReleaseBinding(ctx context.Context, home, root, version string, opts RehydrateOptions) error {
	if !IsCanonicalReleaseVersion(version) {
		return errors.New("cannot repair noncanonical release version")
	}
	if runtime.GOOS == "windows" {
		local := os.Getenv("LOCALAPPDATA")
		if local != "" && !launcherRootOwned(root, local) {
			return errors.New("install root is outside the CSX-owned path")
		}
	}
	if err := SafeLauncherRepairTree(root, version); err != nil {
		return err
	}
	m, err := VerifyInstalledStableRelease(ctx, version, opts.HTTP)
	if err != nil {
		return fmt.Errorf("verify signed release: %w", err)
	}
	if m.Version != version {
		return errors.New("signed release manifest version mismatch")
	}
	asset, err := CurrentAsset(m)
	if err != nil {
		return err
	}
	d := launcher.Descriptor{Version: m.Version, SHA256: asset.SHA256, Sequence: m.Sequence}
	opts.Force = true
	if err := launcher.VerifyPayload(root, d); err != nil {
		if err := refetchPayload(ctx, root, d, asset.Arch, opts); err != nil {
			return fmt.Errorf("refetch payload: %w", err)
		}
	}
	old, readErr := launcher.Read(root)
	next := launcher.Active{Schema: launcher.Schema, Current: d}
	if readErr == nil {
		for _, candidate := range []*launcher.Descriptor{&old.Current, old.Previous} {
			if candidate == nil || (candidate.Version == d.Version && candidate.SHA256 == d.SHA256) {
				continue
			}
			if old.RollbackHold != nil && candidate.Version == old.RollbackHold.Version && candidate.SHA256 == old.RollbackHold.SHA256 {
				continue
			}
			if launcher.VerifyPayload(root, *candidate) == nil {
				verified := *candidate
				next.Previous = &verified
				break
			}
		}
		if old.RollbackHold != nil && (old.RollbackHold.Version != d.Version || old.RollbackHold.SHA256 != d.SHA256) {
			hold := *old.RollbackHold
			next.RollbackHold = &hold
		}
	}
	if err := launcher.Write(root, next); err != nil {
		return fmt.Errorf("write active pointer: %w", err)
	}
	if home != "" {
		if in, err := LoadInstall(home); err == nil && in.Kind == "launcher" {
			if newPayload, err := launcher.PayloadPath(root, d.Version); err == nil {
				in.ExecutablePath = newPayload
				_ = writeJSONAtomic(InstallPath(home), in)
			}
		}
		if st, err := LoadState(home); err == nil {
			st.HighestVersion = m.Version
			if m.Sequence > st.HighestSequence {
				st.HighestSequence = m.Sequence
			}
			_ = SaveState(home, st)
		}
	}
	if asset.OS == "windows" && asset.LauncherSHA256 != "" {
		_, _ = (&Client{HTTP: opts.HTTP}).replaceLauncherIfStale(ctx, root, asset)
	}
	if err := launcher.VerifyPayload(root, d); err != nil {
		return fmt.Errorf("repaired payload failed verification: %w", err)
	}
	return nil
}

