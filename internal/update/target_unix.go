//go:build !windows

package update

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func validateInstallTarget(exe string) error {
	fi, err := os.Lstat(exe)
	if err != nil {
		return fmt.Errorf("update: inspect executable: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("update: automatic replacement refuses a symlink executable")
	}
	dir := filepath.Dir(exe)
	di, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if di.Mode().Perm()&0o022 != 0 {
		return errors.New("update: install directory is group/world writable")
	}
	if st, ok := di.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Geteuid() {
		return errors.New("update: install directory is not owned by the current user")
	}
	return nil
}

func syncInstallDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
