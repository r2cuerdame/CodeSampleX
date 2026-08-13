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

// PipeName derives the per-home named pipe: \\.\pipe\csx-daemon-{hash of
// home}. Hashing the (normalized) home keeps two daemons over different
// homes — e.g. parallel tests — from colliding in the global pipe
// namespace.
func PipeName(home string) string {
	norm := strings.ToLower(filepath.Clean(home))
	sum := sha256.Sum256([]byte(norm))
	return `\\.\pipe\csx-daemon-` + hex.EncodeToString(sum[:])[:16]
}

// listenAux opens the Windows named-pipe listener serving the same mux as
// the TCP listener.
func listenAux(home string) (net.Listener, error) {
	return winio.ListenPipe(PipeName(home), nil)
}

// ipcTransport dials the daemon over its named pipe.
func ipcTransport(home string) *http.Transport {
	pipe := PipeName(home)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return winio.DialPipeContext(ctx, pipe)
		},
	}
}

// detachSysProcAttr detaches a spawned "csx daemon run" from this console
// so it survives the parent (CREATE_NEW_PROCESS_GROUP + DETACHED_PROCESS).
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
}
