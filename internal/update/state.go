package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/launcher"
)

type Install struct {
	Schema         int    `json:"schema"`
	Kind           string `json:"kind"`
	ExecutablePath string `json:"executablePath"`
	InstallRoot    string `json:"installRoot,omitempty"`
	LauncherPath   string `json:"launcherPath,omitempty"`
}

type State struct {
	Schema              int       `json:"schema"`
	HighestSequence     uint64    `json:"highestSequence,omitempty"`
	HighestVersion      string    `json:"highestVersion,omitempty"`
	LastCheck           time.Time `json:"lastCheck,omitempty"`
	NextCheck           time.Time `json:"nextCheck,omitempty"`
	ConsecutiveFailures int       `json:"consecutiveFailures,omitempty"`
	PendingRestart      string    `json:"pendingRestart,omitempty"`
	PreviousPath        string    `json:"previousPath,omitempty"`
	LastError           string    `json:"lastError,omitempty"`
}

func updateDir(home string) string   { return filepath.Join(home, "update") }
func InstallPath(home string) string { return filepath.Join(updateDir(home), "install.json") }
func StatePath(home string) string   { return filepath.Join(updateDir(home), "state.json") }

func AdoptStandalone(home, executable string) error {
	abs, err := filepath.Abs(executable)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if root := os.Getenv("CSX_LAUNCHER_ROOT"); root != "" {
		root, err = filepath.Abs(root)
		if err != nil {
			return err
		}
		a, err := launcher.Load(root)
		if err != nil {
			return fmt.Errorf("update: validate launcher install: %w", err)
		}
		payload, err := launcher.PayloadPath(root, a.Current.Version)
		if err != nil {
			return err
		}
		payload, _ = filepath.Abs(payload)
		if !strings.EqualFold(filepath.Clean(abs), filepath.Clean(payload)) {
			return errors.New("update: running payload does not match launcher's active descriptor")
		}
		launchPath := filepath.Join(root, "csx.exe")
		if fi, err := os.Lstat(launchPath); err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
			return errors.New("update: stable launcher is not a regular file")
		}
		return writeJSONAtomic(InstallPath(home), Install{Schema: 1, Kind: "launcher", ExecutablePath: abs, InstallRoot: root, LauncherPath: launchPath})
	}
	return writeJSONAtomic(InstallPath(home), Install{Schema: 1, Kind: "standalone", ExecutablePath: abs})
}

func LoadInstall(home string) (Install, error) {
	var in Install
	err := readJSON(InstallPath(home), &in)
	if err != nil {
		return in, err
	}
	if in.Schema != 1 || (in.Kind != "standalone" && in.Kind != "launcher") || in.ExecutablePath == "" {
		return Install{}, errors.New("update: install ownership marker is invalid")
	}
	return in, nil
}

func OwnsExecutable(home, executable string) (bool, error) {
	in, err := ResolveInstall(home, executable)
	if err != nil {
		return false, err
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if in.Kind == "launcher" {
		a, err := launcher.Load(in.InstallRoot)
		if err != nil {
			return false, err
		}
		for _, d := range []*launcher.Descriptor{&a.Current, a.Previous} {
			if d == nil {
				continue
			}
			payload, err := launcher.PayloadPath(in.InstallRoot, d.Version)
			if err != nil {
				return false, err
			}
			if strings.EqualFold(filepath.Clean(abs), filepath.Clean(payload)) {
				return true, nil
			}
		}
		return false, nil
	}
	return filepath.Clean(abs) == filepath.Clean(in.ExecutablePath), nil
}

// ResolveInstall uses the per-profile marker when present, then falls back to
// the stable launcher's verified environment. The latter is install-scoped,
// so a second CSX_HOME/profile still resolves the same trusted launcher.
func ResolveInstall(home, executable string) (Install, error) {
	root, launchPath := os.Getenv("CSX_LAUNCHER_ROOT"), os.Getenv("CSX_LAUNCHER_PATH")
	if root != "" && launchPath != "" {
		if in, err := resolveLauncherEnvironment(executable, root, launchPath); err == nil {
			return in, nil
		}
	}
	if in, err := LoadInstall(home); err == nil {
		return in, nil
	}
	return Install{}, errors.New("update: install ownership marker is unavailable")
}

func resolveLauncherEnvironment(executable, root, launchPath string) (Install, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return Install{}, err
	}
	local := os.Getenv("LOCALAPPDATA")
	if local == "" || !strings.EqualFold(filepath.Clean(rootAbs), filepath.Clean(filepath.Join(local, "csx"))) || !strings.EqualFold(filepath.Clean(launchPath), filepath.Join(rootAbs, "csx.exe")) {
		return Install{}, errors.New("update: launcher environment is outside the first-party install root")
	}
	fi, err := os.Lstat(launchPath)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode()&os.ModeSymlink != 0 {
		return Install{}, errors.New("update: stable launcher is invalid")
	}
	a, err := launcher.Load(rootAbs)
	if err != nil {
		return Install{}, err
	}
	if err := launcher.Validate(rootAbs, a); err != nil {
		return Install{}, err
	}
	seq, err := strconv.ParseUint(os.Getenv("CSX_ACTIVE_SEQUENCE"), 10, 64)
	if err != nil {
		return Install{}, errors.New("update: launcher descriptor environment is invalid")
	}
	wantVersion, wantHash := os.Getenv("CSX_PAYLOAD_VERSION"), os.Getenv("CSX_ACTIVE_SHA256")
	exeAbs, err := filepath.Abs(executable)
	if err != nil {
		return Install{}, err
	}
	for _, d := range []*launcher.Descriptor{&a.Current, a.Previous} {
		if d == nil || d.Version != wantVersion || d.SHA256 != wantHash || d.Sequence != seq {
			continue
		}
		payload, _ := launcher.PayloadPath(rootAbs, d.Version)
		if strings.EqualFold(filepath.Clean(exeAbs), filepath.Clean(payload)) {
			return Install{Schema: 1, Kind: "launcher", ExecutablePath: exeAbs, InstallRoot: rootAbs, LauncherPath: filepath.Join(rootAbs, "csx.exe")}, nil
		}
	}
	return Install{}, errors.New("update: running payload does not match the verified launcher descriptor")
}

func StableExecutable(home, executable string) (string, error) {
	in, err := ResolveInstall(home, executable)
	if err != nil {
		return "", err
	}
	owned, err := OwnsExecutable(home, executable)
	if err != nil || !owned {
		return "", errors.New("update: executable is not owned by this install")
	}
	if in.Kind == "launcher" {
		return in.LauncherPath, nil
	}
	return executable, nil
}

func LoadState(home string) (State, error) {
	st := State{Schema: 1}
	if err := readJSON(StatePath(home), &st); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return st, nil
		}
		return State{}, err
	}
	if st.Schema != 1 {
		return State{}, fmt.Errorf("update: unsupported state schema %d", st.Schema)
	}
	return st, nil
}

func SaveState(home string, st State) error {
	st.Schema = 1
	return writeJSONAtomic(StatePath(home), st)
}

func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return recoverDataFile(path, out, err)
		}
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("update: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSONAtomic(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(raw)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return writeErr
	}
	if err := replaceDataFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncInstallDir(filepath.Dir(path))
}
