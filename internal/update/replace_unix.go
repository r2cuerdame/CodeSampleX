//go:build !windows

package update

import (
	"io"
	"os"
)

func replaceExecutable(current, staged, previous string) error {
	if err := copyFile(current, previous); err != nil {
		return err
	}
	if err := os.Rename(staged, current); err != nil {
		return err
	}
	return nil
}

func rollbackExecutable(current, previous string) error {
	failed := current + ".failed"
	_ = os.Remove(failed)
	if err := os.Rename(current, failed); err != nil {
		return err
	}
	if err := os.Rename(previous, current); err != nil {
		_ = os.Rename(failed, current)
		return err
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}
