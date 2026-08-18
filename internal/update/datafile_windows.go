//go:build windows

package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func replaceDataFile(staged, target string) error {
	// Go implements same-volume replacement with MoveFileExW and
	// MOVEFILE_REPLACE_EXISTING. A single rename has no target-missing window.
	return os.Rename(staged, target)
}

func recoverDataFile(path string, out any, original error) error {
	old := path + ".old"
	raw, err := os.ReadFile(old)
	if err != nil {
		return original
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("update: parse legacy recovery %s: %w", filepath.Base(old), err)
	}
	if err := os.Rename(old, path); err != nil {
		return fmt.Errorf("update: recover legacy state file: %w", err)
	}
	return nil
}
