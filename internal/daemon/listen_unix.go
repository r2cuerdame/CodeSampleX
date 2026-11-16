//go:build !windows

package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

func SocketPath(home string) string { return filepath.Join(home, "daemon.sock") }

func listenAux(home string) (net.Listener, error) {
	path := SocketPath(home)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", path)
}

func ipcTransport(home string) *http.Transport {
	sock := SocketPath(home)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
}

func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

func detachFallbackSysProcAttr() *syscall.SysProcAttr {
	return nil
}
