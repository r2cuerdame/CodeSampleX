//go:build windows

package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"syscall"

	winio "github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func PipeName(home string) string {
	norm := strings.ToLower(filepath.Clean(home))
	sum := sha256.Sum256([]byte(norm))
	return `\\.\pipe\csx-daemon-` + hex.EncodeToString(sum[:])[:16]
}

func listenAux(home string) (net.Listener, error) {
	return winio.ListenPipe(PipeName(home), nil)
}

func ipcTransport(home string) *http.Transport {
	pipe := PipeName(home)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, pipe)
		},
	}
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW | windows.CREATE_BREAKAWAY_FROM_JOB,
		HideWindow:    true,
	}
}

func detachFallbackSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
}
