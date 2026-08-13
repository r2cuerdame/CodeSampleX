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

// SocketPath is the unix-domain socket the daemon serves next to its TCP
// listener: $home/daemon.sock.
func SocketPath(home string) string { return filepath.Join(home, "daemon.sock") }

// listenAux opens the unix socket listener serving the same mux as the
// TCP listener. A stale socket file from a crashed daemon is removed
// first — the single-instance lock already guarantees no live daemon owns
// it. Go unlinks the socket again on Close.
func listenAux(home string) (net.Listener, error) {
	path := SocketPath(home)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return net.Listen("unix", path)
}

// ipcTransport dials the daemon over its unix socket.
func ipcTransport(home string) *http.Transport {
	sock := SocketPath(home)
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
}

// detachSysProcAttr detaches a spawned "csx daemon run" from this session
// (setsid) so it survives the parent.
func detachSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
