package daemon

import (
	"context"
	"testing"
	"time"
)

// Asking a home with no daemon must fail quickly, not hang.
//
// The isolation fix routed a home with no published address to that home's
// own IPC socket. On Windows that is a named pipe, and winio.DialPipeContext
// RETRIES while the pipe does not exist, until its context is done — so a
// missing daemon stopped being an instant connection-refused and became a
// wait as long as the client's own timeout.
//
// Every read command goes through this: csx stats, csx search, csx sync and
// the build-failure hook all build a client this way. A hook that pauses a
// developer's failed build for tens of seconds is worse than the wrong-home
// answer it replaced.
func TestAskingAHomeWithNoDaemonFailsFast(t *testing.T) {
	home := newTestHome(t, nil) // no daemon started

	c, err := NewClient(home)
	if err != nil {
		return // an error here is a fast failure, which is the point
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := c.Status(context.Background())
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a home with no daemon reported a status")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("took %v to report no daemon; a read command must not stall on a missing one", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("asking a home with no daemon hung for 10s")
	}
}
