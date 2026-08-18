//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateInstallTarget(exe string) error {
	fi, err := os.Lstat(exe)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("update: automatic replacement refuses a symlink executable")
	}
	root := os.Getenv("LOCALAPPDATA")
	if root == "" {
		return errors.New("update: LOCALAPPDATA is unavailable; cannot verify first-party install ownership")
	}
	want := filepath.Join(root, "csx", "csx.exe")
	abs, err := filepath.Abs(exe)
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(abs), filepath.Clean(want)) {
		return fmt.Errorf("update: Windows automatic replacement is limited to the first-party install path %s", want)
	}
	return nil
}

func syncInstallDir(string) error { return nil }
