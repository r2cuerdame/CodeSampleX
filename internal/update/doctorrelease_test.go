package update

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSafeLauncherRepairTreeRejectsLinkedPayloadDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "payloads")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if err := SafeLauncherRepairTree(root, "v1.2.3"); err == nil {
		t.Fatal("linked payload directory accepted for repair")
	}
}
