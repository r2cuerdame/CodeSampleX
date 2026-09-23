package update

import (
	"context"
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

func TestRepairReleaseBindingRejectsNoncanonicalVersion(t *testing.T) {
	err := RepairReleaseBinding(context.Background(), "", t.TempDir(), "dev", RehydrateOptions{})
	if err == nil {
		t.Fatal("expected error for noncanonical version")
	}
}

func TestRepairReleaseBindingRejectsUntrustedRoot(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	err := RepairReleaseBinding(context.Background(), "", filepath.Join(t.TempDir(), "other"), "v1.2.3", RehydrateOptions{})
	if err == nil {
		t.Fatal("expected error for untrusted install root")
	}
}
