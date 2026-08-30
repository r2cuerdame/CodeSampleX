package daemon

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := config.EnsureHome(home); err != nil {
		t.Fatal(err)
	}
	return home
}

// A client asked about one home must never be answered by another home's
// daemon.
//
// Every home carries the same default daemonPort (48619), and BaseURLFor fell
// back to it whenever the home had no daemon.addr of its own. On a farm node
// running three worker slots plus a default home, only the first daemon can
// bind that port — the rest exit on "listen tcp" — so the three homes without
// a daemon all resolved to the one that had it, and every csx stats, search,
// sync and hook for those homes was answered from a store that was not theirs.
//
// It reads as a data loss: three worker homes reporting the fourth's numbers
// look identical to three homes that were wiped, which is exactly how it was
// reported from production — 28/14 hits and 6 known packages, the same in
// every home, on a node whose homes each held days of their own evidence.
func TestTwoHomesNeverResolveToTheSameDaemon(t *testing.T) {
	a, b := freshHome(t), freshHome(t)

	urlA, errA := BaseURLFor(a)
	urlB, errB := BaseURLFor(b)

	// Neither home has a daemon, so neither may claim an address. What must
	// never happen is both claiming the SAME one.
	if errA == nil && errB == nil && urlA == urlB {
		t.Fatalf("two homes with no daemon both resolve to %s — one home's daemon answers for the other", urlA)
	}
}

// The address a home does own is still honoured: this is about not inventing
// one, not about refusing a real daemon.
func TestAHomeWithItsOwnDaemonAddressUsesIt(t *testing.T) {
	home := freshHome(t)
	if err := os.WriteFile(filepath.Join(home, "daemon.addr"), []byte("127.0.0.1:51999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := BaseURLFor(home)
	if err != nil {
		t.Fatalf("BaseURLFor: %v", err)
	}
	if got != "http://127.0.0.1:51999" {
		t.Errorf("BaseURLFor = %q, want the address this home wrote", got)
	}
}

// A second home on one machine must get a daemon of its own.
//
// Every home carries the same configured daemonPort, so only the first daemon
// on a machine can bind it. The rest returned "daemon: listen tcp" and exited,
// which is not merely a missing status endpoint: the upload loop, the syncer
// and the verification loop all live inside a running daemon, so those homes
// stopped draining their own evidence entirely.
//
// A farm node runs three worker slots plus a default home. Three of the four
// were in this state, and because a client with no published address fell back
// to the same shared port, asking any of them returned the fourth's numbers —
// so the outage looked like three wiped stores rather than three dead daemons.
//
// The port is occupied by a plain listener rather than by a first daemon: the
// test homes default to an ephemeral port so they can run in parallel, and a
// version of this that let them do that never collided at all and passed
// against the bug.
func TestADaemonStartsWhenItsConfiguredPortIsTaken(t *testing.T) {
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	taken := squatter.Addr().(*net.TCPAddr).Port

	home := newTestHome(t, func(c *config.Config) { c.DaemonPort = taken })
	d, _ := startDaemon(t, home) // fails here if a taken port is fatal

	if d.BaseURL() == "http://"+squatter.Addr().String() {
		t.Fatal("the daemon claims the address something else is serving")
	}
	got, err := BaseURLFor(home)
	if err != nil {
		t.Fatalf("BaseURLFor: %v", err)
	}
	if got != d.BaseURL() {
		t.Errorf("home resolves to %s, its daemon serves %s", got, d.BaseURL())
	}
}
