//go:build windows

package daemon

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestDetachSysProcAttrUsesCreateNoWindow(t *testing.T) {
	attr := detachSysProcAttr()
	if attr == nil {
		t.Fatal("detachSysProcAttr returned nil")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NO_WINDOW set", attr.CreationFlags)
	}
	if attr.CreationFlags&windows.DETACHED_PROCESS != 0 {
		t.Errorf("CreationFlags = %#x, must not set DETACHED_PROCESS alongside CREATE_NO_WINDOW", attr.CreationFlags)
	}
	if !attr.HideWindow {
		t.Error("expected HideWindow to be true")
	}
}
