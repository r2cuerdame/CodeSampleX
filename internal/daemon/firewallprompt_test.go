package daemon

import (
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

// R2C-84. On Windows the daemon is not run from a stable path. The stable
// thing is the launcher, csx.exe; the process that actually serves is the
// payload under payloads/<version>/csx-payload.exe, and that path changes
// on every single upgrade. Windows Defender Firewall identifies a program
// by its executable path, so a listener bound to an unspecified host would
// raise the consent dialog again after every update, forever, and no allow
// decision could ever be remembered.
//
// The daemon binds loopback, which the firewall never asks about. That is
// the property that keeps upgrades silent, and it is not obvious from the
// call site — net.Listen("tcp", ":48619") is one deleted argument away —
// so it is asserted here rather than assumed.
func TestDaemonListenerNeverBindsAnAddressTheFirewallWouldPromptFor(t *testing.T) {
	// A fresh install, then a second daemon standing in for the upgraded
	// payload: a different process at a different executable path, which is
	// exactly what an update produces.
	for _, name := range []string{"fresh install", "after upgrade"} {
		t.Run(name, func(t *testing.T) {
			d, _ := startDaemon(t, newTestHome(t, nil))
			assertLoopback(t, "daemon TCP listener", strings.TrimPrefix(d.BaseURL(), "http://"))

			// The address file is what CLI clients dial, so it has to carry
			// the same loopback address rather than a routable one.
			raw, err := os.ReadFile(addrFile(d.Home))
			if err != nil {
				t.Fatalf("read daemon.addr: %v", err)
			}
			assertLoopback(t, "daemon.addr", strings.TrimSpace(string(raw)))
		})
	}
}

// Community mode is the mode that talks to the network, and it still does
// not open a listening socket the firewall would ask about: serving samples
// to other peers is a separate switch, off until the user sets it. If that
// gate ever regressed, every community user on Windows would meet the
// consent dialog once per upgrade.
func TestPeerListenerStaysClosedUntilExplicitlyEnabled(t *testing.T) {
	if def := config.Default(); def.PeerListen {
		t.Fatal("config.Default() enables peerListen; serving the local network must stay an explicit choice")
	}
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.PeerPort = freePort(t)
	})
	d, _ := startDaemon(t, home)
	assertLoopback(t, "daemon TCP listener", strings.TrimPrefix(d.BaseURL(), "http://"))

	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.PeerPort))
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err == nil {
		conn.Close()
		t.Fatalf("something is listening on the peer port %s with peerListen off", addr)
	}
}

func assertLoopback(t *testing.T, what, addr string) {
	t.Helper()
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("%s: cannot parse %q: %v", what, addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("%s bound the name %q rather than a loopback address", what, host)
	}
	if ip.IsUnspecified() {
		t.Fatalf("%s bound %s — an unspecified host binds every interface, which raises the "+
			"Windows firewall dialog under an executable path that changes on every upgrade", what, host)
	}
	if !ip.IsLoopback() {
		t.Fatalf("%s bound %s, which is reachable from the local network; it must be loopback", what, host)
	}
}

// freePort reserves and releases a port so the peer-port probe cannot
// collide with an unrelated listener on a busy machine.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
