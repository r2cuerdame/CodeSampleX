//go:build windows

package update

import (
	"errors"
	"io"
	"os"
	"time"
)

func replaceExecutable(current, staged, previous string) error {
	return errors.New("update: direct Windows executable replacement is disabled; stable launcher ownership is required")
}

func rollbackExecutable(current, previous string) error {
	failed := current + ".failed"
	if err := copyFileWindows(current, failed); err != nil {
		return err
	}
	return retryRename(previous, current)
}

func copyFileWindows(src, dst string) error {
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
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := retryRename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func retryRename(old, new string) error {
	var err error
	for i := 0; i < 10; i++ {
		if err = os.Rename(old, new); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 50 * time.Millisecond)
	}
	return err
}
