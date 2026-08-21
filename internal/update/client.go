package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

const (
	maxManifestBytes = 1 << 20
	maxBinaryBytes   = 256 << 20
	normalCheckEvery = 6 * time.Hour
)

var ErrPolicyDisabled = errors.New("update: automatic update policy was disabled")

type Result struct {
	CurrentVersion        string
	LatestVersion         string
	Available             bool
	Applied               bool
	RestartRequired       bool
	ManualInstallRequired bool
	RollbackHeld          bool
	PreviousPath          string
	NextCheck             time.Time
}

type Client struct {
	Home                string
	CurrentVersion      string
	Executable          string
	ManifestURL         string
	Channel             string
	PublicKey           ed25519.PublicKey
	HTTP                *http.Client
	Now                 func() time.Time
	SelfTest            func(context.Context, string, string) error
	Replace             func(string, string, string) error
	ValidateTarget      func(string) error
	SyncDir             func(string) error
	LoadState           func(string) (State, error)
	SaveState           func(string, State) error
	CheckApplySupport   func() error
	Preflight           func() error
	Automatic           bool
	directApplyForTests bool
}

func AutoEnabled(mode, policy string) bool {
	if mode != "community" {
		return false
	}
	switch policy {
	case "on":
		return true
	case "off":
		return false
	case "", "auto":
		return true
	default:
		return false
	}
}

func (c *Client) Check(ctx context.Context, apply bool) (res Result, retErr error) {
	res.CurrentVersion = c.CurrentVersion
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	unlock, err := acquireLock(c.Home, now)
	if err != nil {
		return res, err
	}
	defer unlock()
	loadState := c.LoadState
	if loadState == nil {
		loadState = LoadState
	}
	saveState := c.SaveState
	if saveState == nil {
		saveState = SaveState
	}
	st, err := loadState(c.Home)
	if err != nil {
		return res, err
	}
	if c.Preflight != nil {
		if err := c.Preflight(); err != nil {
			return res, err
		}
	}
	if runtime.GOOS == "windows" && !c.directApplyForTests {
		if in, installErr := ResolveInstall(c.Home, c.Executable); installErr == nil && in.Kind == "launcher" {
			a, activeErr := launcher.Load(in.InstallRoot)
			if activeErr != nil {
				return res, activeErr
			}
			if err := launcher.Validate(in.InstallRoot, a); err != nil {
				return res, err
			}
			mergeLauncherFloor(&st, a)
		}
	}
	defer func() {
		st.LastCheck = now
		if retErr != nil {
			st.ConsecutiveFailures++
			st.LastError = retErr.Error()
			st.NextCheck = now.Add(backoff(st.ConsecutiveFailures, c.Home))
		} else {
			st.ConsecutiveFailures = 0
			st.LastError = ""
			st.NextCheck = now.Add(jitter(normalCheckEvery, c.Home))
		}
		res.NextCheck = st.NextCheck
		if err := saveState(c.Home, st); err != nil {
			if retErr == nil {
				retErr = err
			} else {
				retErr = fmt.Errorf("%v; update state save failed: %w", retErr, err)
			}
		}
	}()

	pub := c.PublicKey
	if len(pub) == 0 {
		pub, err = EmbeddedPublicKey()
		if err != nil {
			return res, err
		}
	}
	manifestURL := c.ManifestURL
	if manifestURL == "" {
		manifestURL = DefaultManifestURL
	}
	channel := c.Channel
	if channel == "" {
		channel = DefaultChannel
	}
	// An injected HTTP client is a test seam (httptest URLs are deliberately
	// not production-trusted). The production transport always validates the
	// initial URL and every redirect before making a request.
	if c.HTTP == nil {
		if err := validateManifestURL(manifestURL, channel); err != nil {
			return res, err
		}
	}
	raw, err := c.get(ctx, manifestURL, maxManifestBytes)
	if err != nil {
		return res, err
	}
	m, err := VerifyEnvelope(raw, pub, now, channel)
	if err != nil {
		return res, err
	}
	res.LatestVersion = m.Version
	if m.Sequence < st.HighestSequence {
		return res, fmt.Errorf("update: refused replayed manifest sequence %d below %d", m.Sequence, st.HighestSequence)
	}
	if st.HighestVersion != "" {
		if cmp, cmpErr := CompareVersions(m.Version, st.HighestVersion); cmpErr != nil || cmp < 0 {
			return res, fmt.Errorf("update: refused release rollback from %s to %s", st.HighestVersion, m.Version)
		}
	}
	if m.MinUpdaterVersion != "" {
		cmp, cmpErr := CompareVersions(c.CurrentVersion, m.MinUpdaterVersion)
		if cmpErr != nil || cmp < 0 {
			return res, fmt.Errorf("update: %s requires updater %s; rerun the official installer", m.Version, m.MinUpdaterVersion)
		}
	}
	cmp, err := CompareVersions(c.CurrentVersion, m.Version)
	if err != nil {
		return res, fmt.Errorf("update: current build %q is not an updatable release", c.CurrentVersion)
	}
	st.HighestSequence = max(st.HighestSequence, m.Sequence)
	if st.HighestVersion == "" {
		st.HighestVersion = c.CurrentVersion
	}
	if cmp >= 0 {
		return res, nil
	}
	res.Available = true
	if !apply {
		return res, nil
	}
	if c.CheckApplySupport != nil {
		if err := c.CheckApplySupport(); err != nil {
			res.ManualInstallRequired = true
			return res, nil
		}
	}

	exe := c.Executable
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return res, fmt.Errorf("update: locate executable: %w", err)
		}
	}
	owned, err := OwnsExecutable(c.Home, exe)
	if err != nil || !owned {
		if runtime.GOOS == "windows" {
			res.ManualInstallRequired = true
			return res, nil
		}
		return res, errors.New("update: automatic replacement refused because this executable is not a standalone install owned by csx; run the official installer or use your MCP client updater")
	}
	asset, err := m.AssetFor(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return res, err
	}
	if err := validateSignedAssetURL(asset.URL, m.Version, asset.OS, asset.Arch); err != nil {
		return res, err
	}
	if runtime.GOOS == "windows" && !c.directApplyForTests {
		in, loadErr := ResolveInstall(c.Home, exe)
		if loadErr != nil || in.Kind != "launcher" {
			res.ManualInstallRequired = true
			return res, nil
		}
		launcherVersion := os.Getenv("CSX_LAUNCHER_VERSION")
		if asset.MinLauncherVersion != "" {
			cmpLauncher, cmpErr := CompareVersions(launcherVersion, asset.MinLauncherVersion)
			if cmpErr != nil || cmpLauncher < 0 {
				res.ManualInstallRequired = true
				return res, nil
			}
		}
		unlockInstall, lockErr := acquireNamedLock(filepath.Join(in.InstallRoot, ".update.lock"), 5*time.Second)
		if lockErr != nil {
			return res, lockErr
		}
		defer unlockInstall()
		active, activeErr := launcher.Load(in.InstallRoot)
		if activeErr != nil {
			return res, activeErr
		}
		if err := launcher.Validate(in.InstallRoot, active); err != nil {
			return res, err
		}
		mergeLauncherFloor(&st, active)
		if m.Sequence < st.HighestSequence {
			return res, fmt.Errorf("update: refused replayed manifest sequence %d below installed launcher floor %d", m.Sequence, st.HighestSequence)
		}
		if cmpFloor, cmpErr := CompareVersions(m.Version, st.HighestVersion); cmpErr != nil || cmpFloor < 0 {
			return res, fmt.Errorf("update: refused release rollback below installed launcher floor %s", st.HighestVersion)
		}
		if c.Automatic && active.RollbackHold != nil && m.Sequence <= active.RollbackHold.Sequence {
			res.RollbackHeld = true
			return res, nil
		}
		if active.Current.Version == m.Version && active.Current.SHA256 == asset.SHA256 && active.Current.Sequence == m.Sequence {
			st.PendingRestart = m.Version
			res.Applied, res.RestartRequired = true, true
			if active.Previous != nil {
				res.PreviousPath = "active.json:" + active.Previous.Version
				st.PreviousPath = res.PreviousPath
			}
			return res, nil
		}
		staged, downloadErr := c.downloadAsset(ctx, filepath.Join(in.InstallRoot, "csx-payload.exe"), asset)
		if downloadErr != nil {
			return res, downloadErr
		}
		defer os.Remove(staged)
		selfTest := c.SelfTest
		if selfTest == nil {
			selfTest = selfTestBinary
		}
		if err := selfTest(ctx, staged, m.Version); err != nil {
			return res, err
		}
		next, commitErr := launcher.CommitPayload(in.InstallRoot, staged, launcher.Descriptor{Version: m.Version, SHA256: asset.SHA256, Sequence: m.Sequence})
		if commitErr != nil {
			return res, commitErr
		}
		st.HighestVersion, st.PendingRestart = m.Version, m.Version
		if next.Previous != nil {
			st.PreviousPath = "active.json:" + next.Previous.Version
		}
		res.Applied, res.RestartRequired, res.PreviousPath = true, true, st.PreviousPath
		return res, nil
	}
	validateTarget := c.ValidateTarget
	if validateTarget == nil {
		validateTarget = validateInstallTarget
	}
	if err := validateTarget(exe); err != nil {
		return res, err
	}
	// The previous process may have committed the rename but failed to save
	// state. Never replace the same release twice: that would overwrite the
	// genuine previous binary with a copy of the already-new one.
	if digest, digestErr := fileSHA256(exe); digestErr == nil && digest == asset.SHA256 {
		st.HighestVersion = m.Version
		st.PendingRestart = m.Version
		st.PreviousPath = exe + ".previous"
		res.Applied = true
		res.RestartRequired = true
		res.PreviousPath = st.PreviousPath
		return res, nil
	}
	staged, err := c.downloadAsset(ctx, exe, asset)
	if err != nil {
		return res, err
	}
	defer os.Remove(staged)
	selfTest := c.SelfTest
	if selfTest == nil {
		selfTest = selfTestBinary
	}
	if err := selfTest(ctx, staged, m.Version); err != nil {
		return res, err
	}
	previous := exe + ".previous"
	replace := c.Replace
	if replace == nil {
		replace = replaceExecutable
	}
	if err := replace(exe, staged, previous); err != nil {
		return res, fmt.Errorf("update: replace executable: %w", err)
	}
	st.HighestVersion = m.Version
	st.PendingRestart = m.Version
	st.PreviousPath = previous
	res.Applied = true
	res.RestartRequired = true
	res.PreviousPath = previous
	syncDir := c.SyncDir
	if syncDir == nil {
		syncDir = syncInstallDir
	}
	if err := syncDir(filepath.Dir(exe)); err != nil {
		return res, fmt.Errorf("update: sync install directory: %w", err)
	}
	return res, nil
}

func mergeLauncherFloor(st *State, a launcher.Active) {
	for _, d := range []*launcher.Descriptor{&a.Current, a.Previous, a.RollbackHold} {
		if d == nil {
			continue
		}
		if d.Sequence > st.HighestSequence {
			st.HighestSequence = d.Sequence
		}
		if st.HighestVersion == "" {
			st.HighestVersion = d.Version
			continue
		}
		if cmp, err := CompareVersions(d.Version, st.HighestVersion); err == nil && cmp > 0 {
			st.HighestVersion = d.Version
		}
	}
}

func (c *Client) Due() bool {
	st, err := LoadState(c.Home)
	if err != nil || st.NextCheck.IsZero() {
		return true
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	return !now.Before(st.NextCheck)
}

func (c *Client) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				return validateDownloadURL(req.URL)
			},
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("update: download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("update: download %s: HTTP %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength > limit {
		return nil, errors.New("update: download exceeds size limit")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("update: download exceeds size limit")
	}
	return raw, nil
}

func (c *Client) downloadAsset(ctx context.Context, exe string, asset Asset) (string, error) {
	if asset.Size > maxBinaryBytes {
		return "", errors.New("update: binary exceeds size limit")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validateDownloadURL(req.URL)
		}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("update: download %s: %w", asset.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update: download %s: HTTP %d", asset.URL, resp.StatusCode)
	}
	if resp.ContentLength > asset.Size {
		return "", errors.New("update: binary exceeds signed size")
	}

	pattern := ".csx-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	f, err := os.CreateTemp(filepath.Dir(exe), pattern)
	if err != nil {
		return "", fmt.Errorf("update: stage binary: %w", err)
	}
	name := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	if err := f.Chmod(0o700); err != nil {
		_ = f.Close()
		return "", fmt.Errorf("update: stage binary: %w", err)
	}
	h := sha256.New()
	n, writeErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, asset.Size+1))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", fmt.Errorf("update: stage binary: %w", writeErr)
	}
	if n != asset.Size {
		return "", fmt.Errorf("update: binary size %d does not match signed size %d", n, asset.Size)
	}
	if hex.EncodeToString(h.Sum(nil)) != asset.SHA256 {
		return "", errors.New("update: binary SHA-256 does not match signed manifest")
	}
	cleanup = false
	return name, nil
}

func validateManifestURL(raw, channel string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("update: invalid manifest URL: %w", err)
	}
	if err := validateDownloadURL(u); err != nil {
		return err
	}
	want := "csx-update-" + channel + ".json"
	switch strings.ToLower(u.Hostname()) {
	case "github.com":
		if u.Path == "/r2cuerdame/CodeSampleX/releases/latest/download/"+want {
			return nil
		}
	case "codesamplex.dev":
		if u.Path == "/dl/"+want {
			return nil
		}
	}
	return errors.New("update: manifest URL is outside the official CodeSampleX release path")
}

func validateSignedAssetURL(raw, version, osName, arch string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("update: invalid asset URL: %w", err)
	}
	if err := validateDownloadURL(u); err != nil {
		return err
	}
	want := "csx-" + osName + "-" + arch
	if osName == "windows" {
		want += ".exe"
	}
	switch strings.ToLower(u.Hostname()) {
	case "github.com":
		wantPath := "/r2cuerdame/CodeSampleX/releases/download/" + version + "/" + want
		if u.Path != wantPath {
			return fmt.Errorf("update: asset URL path %q does not match signed target %q", u.Path, wantPath)
		}
		return nil
	case "codesamplex.dev":
		if u.Path == "/dl/"+want {
			return nil
		}
	}
	return fmt.Errorf("update: asset URL does not name the exact %s/%s release binary", osName, arch)
}

func validateDownloadURL(u *url.URL) error {
	if u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return errors.New("update: download URL must use plain HTTPS without credentials or a custom port")
	}
	host := strings.ToLower(u.Hostname())
	if host != "github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com" && host != "codesamplex.dev" {
		return fmt.Errorf("update: download host %q is not trusted", host)
	}
	return nil
}

func selfTestBinary(ctx context.Context, path, version string) error {
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(tctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("update: staged binary self-test failed: %w", err)
	}
	if strings.TrimSpace(string(out)) != "csx "+version {
		return fmt.Errorf("update: staged binary reports %q, want %q", strings.TrimSpace(string(out)), "csx "+version)
	}
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxBinaryBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxBinaryBytes {
		return "", errors.New("update: file exceeds size limit")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func jitter(base time.Duration, key string) time.Duration {
	sum := sha256.Sum256([]byte(key))
	// 80%..120%, stable for this installation.
	percent := 80 + int(sum[0])%41
	return time.Duration(int64(base) * int64(percent) / 100)
}

func backoff(failures int, key string) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := 15 * time.Minute
	for i := 1; i < failures && d < 24*time.Hour; i++ {
		d *= 2
	}
	if d > 24*time.Hour {
		d = 24 * time.Hour
	}
	return jitter(d, key+fmt.Sprint(failures))
}

func acquireLock(home string, now time.Time) (func(), error) {
	return acquireLockWithWait(home, now, 5*time.Second)
}

func acquireLockWithWait(home string, _ time.Time, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(updateDir(home), 0o700); err != nil {
		return nil, err
	}
	return acquireNamedLock(filepath.Join(updateDir(home), "update.lock"), wait)
}

func acquireNamedLock(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	tokenRaw := make([]byte, 16)
	if _, err := rand.Read(tokenRaw); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenRaw)
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "%s %d\n", token, os.Getpid())
			_ = f.Close()
			return func() {
				raw, readErr := os.ReadFile(path)
				if readErr == nil && strings.HasPrefix(string(raw), token+" ") {
					_ = os.Remove(path)
				}
			}, nil
		}
		// A lock whose holder is gone is not a lock. Without this, a crash, a
		// kill or a reboot mid-update left the file behind and every later
		// update failed with "another update is still in progress" — for
		// good. Found on a real install: update.lock naming a dead pid while
		// the daemon ran a build fifteen releases old, because auto-update
		// had been failing silently ever since.
		//
		// Removing it only loses a race, never correctness: whoever wins the
		// next O_EXCL owns the lock, and the loser goes round again.
		if lockIsStale(path) {
			_ = os.Remove(path)
			continue
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, errors.New("update: another update is still in progress")
}

// WithLock serializes a consent/config write with update checks. When it
// returns, no check that observed the old policy can begin a later request.
func WithLock(home string, fn func() error) error {
	unlock, err := acquireLockWithWait(home, time.Now(), 6*time.Minute)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

var rollbackSyncDir = syncInstallDir
var rollbackSaveState = SaveState

func Rollback(home, executable string) (string, error) {
	owned, err := OwnsExecutable(home, executable)
	if err != nil || !owned {
		return "", errors.New("update: rollback refused for an unowned executable")
	}
	in, err := ResolveInstall(home, executable)
	if err != nil {
		return "", err
	}
	if in.Kind == "launcher" {
		unlockHome, err := acquireLock(home, time.Now())
		if err != nil {
			return "", err
		}
		defer unlockHome()
		st, err := LoadState(home)
		if err != nil {
			return "", err
		}
		unlockInstall, err := acquireNamedLock(filepath.Join(in.InstallRoot, ".update.lock"), 5*time.Second)
		if err != nil {
			return "", err
		}
		defer unlockInstall()
		next, err := launcher.Rollback(in.InstallRoot)
		if err != nil {
			return "", err
		}
		st.PendingRestart = next.Current.Version
		st.PreviousPath = "active.json"
		if err := rollbackSaveState(home, st); err != nil {
			return in.LauncherPath, fmt.Errorf("update: rollback pointer committed but state save failed; restart required: %w", err)
		}
		return in.LauncherPath, nil
	}
	if err := validateInstallTarget(executable); err != nil {
		return "", err
	}
	previous := executable + ".previous"
	unlock, err := acquireLock(home, time.Now())
	if err != nil {
		return "", err
	}
	defer unlock()
	st, err := LoadState(home)
	if err != nil {
		return "", err
	}
	rollbackVersion, err := rollbackBinaryVersion(previous)
	if err != nil {
		return "", err
	}
	if err := rollbackExecutable(executable, previous); err != nil {
		return "", err
	}
	if err := rollbackSyncDir(filepath.Dir(executable)); err != nil {
		return executable, fmt.Errorf("update: rollback restored the previous binary but directory sync failed; restart required: %w", err)
	}
	st.PendingRestart = rollbackVersion
	st.PreviousPath = ""
	if err := rollbackSaveState(home, st); err != nil {
		return executable, fmt.Errorf("update: rollback restored the previous binary but state save failed; restart required: %w", err)
	}
	return executable, nil
}

func validateRollbackBinary(path string) error {
	_, err := rollbackBinaryVersion(path)
	return err
}

func rollbackBinaryVersion(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("update: no previous binary is available")
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
		return "", errors.New("update: previous binary is not a regular file")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("update: previous binary self-test failed: %w", err)
	}
	reported := strings.TrimSpace(string(out))
	if !strings.HasPrefix(reported, "csx ") || !IsCanonicalReleaseVersion(strings.TrimPrefix(reported, "csx ")) {
		return "", fmt.Errorf("update: previous binary reports invalid version %q", reported)
	}
	return strings.TrimPrefix(reported, "csx "), nil
}

// AcknowledgeActivation clears a restart notice only from a newly launched
// binary that matches the installed target (or the first process after a
// rollback). It never removes the preserved previous executable.
func AcknowledgeActivation(home, currentVersion string) error {
	unlock, err := acquireLock(home, time.Now())
	if err != nil {
		return err
	}
	defer unlock()
	st, err := LoadState(home)
	if err != nil {
		return err
	}
	if st.PendingRestart == "" {
		return nil
	}
	cmp, cmpErr := CompareVersions(currentVersion, st.PendingRestart)
	if cmpErr != nil || cmp != 0 {
		return nil
	}
	st.PendingRestart = ""
	return SaveState(home, st)
}

// namelessLockAbandonedAfter applies only to a lock that names nobody — a
// truncated or garbled file. There is no owner to protect and no other way
// for it to ever be released, so after long enough that no update could
// still be running it is taken over. A day is far past any real download.
const namelessLockAbandonedAfter = 24 * time.Hour

// lockIsStale reports whether a lock file is one nobody is coming back for.
//
// A live owner is never overruled, however old its lock looks. That is a
// deliberate decision and it is right: an update on a slow link can hold this
// for a long time, and trampling it would corrupt the very thing the lock
// exists to protect. Age alone proves nothing about whether somebody is still
// working.
//
// What age cannot excuse, death can. A lock whose holder is gone is not a
// lock, and without this every later update failed with "another update is
// still in progress" — for good. Found on a real install: update.lock naming
// a dead pid while the daemon ran a build fifteen releases old, because
// auto-update had been failing silently ever since.
func lockIsStale(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false // gone already; the next O_EXCL settles it
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		// Names nobody. It may still be a lock mid-write, so it is left alone
		// until it is far too old to be one.
		fi, statErr := os.Stat(path)
		return statErr == nil && time.Since(fi.ModTime()) > namelessLockAbandonedAfter
	}
	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return false
	}
	return !lockPidAlive(pid)
}

// currentPid is a seam so the per-platform liveness checks can share one
// answer for "is that us".
func currentPid() int { return os.Getpid() }
