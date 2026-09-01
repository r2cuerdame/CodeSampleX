package daemon

import (
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

// unclosableListener is a listener whose Close never returns.
//
// That is not a hypothetical. go-winio v0.6.2 -- the newest there is -- sends
// its close signal on an UNBUFFERED channel exactly once, and
// makeConnectedServerPipe's own select can consume it instead of the listener
// routine. When the aborted connect then returns anything other than nil or
// ErrFileClosed, the routine does not set closed, goes back to its select,
// and nothing ever closes doneCh. win32PipeListener.Close waits on doneCh
// (pipe.go:578), so it waits forever.
type unclosableListener struct {
	net.Listener
	wedged chan struct{}
}

func newUnclosableListener(t *testing.T) *unclosableListener {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return &unclosableListener{Listener: inner, wedged: make(chan struct{})}
}

// Accept never returns, so Serve stays inside it and stays TRACKED -- which
// is what puts the wedged listener in front of Shutdown's closeListenersLocked
// rather than behind an already-unwound Serve.
func (l *unclosableListener) Accept() (net.Conn, error) {
	<-l.wedged
	return nil, errors.New("listener closed")
}

func (l *unclosableListener) Close() error {
	<-l.wedged // never
	return nil
}

// A listener that will not close must not decide whether the daemon stops.
//
// Measured on the v0.1.88 release runner. The stack named every frame:
//
//	Run -> stopServer -> Shutdown -> closeListenersLocked   [holds srv.mu]
//	    -> onceCloseListener.Close -> winio pipe Close      [chan receive]
//	Run.func2 -> Serve(pipe) -> trackListener               [sync.Mutex.Lock]
//
// Shutdown closes listeners while holding srv.mu, the pipe's Close never
// returns, and the pipe's own Serve is left wanting that mutex to untrack
// itself. No budget rescued it: Shutdown's grace bounds the drain that
// happens AFTER the listeners are closed, so it was never reached, and
// neither was the caller's timeout above it.
//
// Reordering does not fix this -- closing the pipe first hangs in the same
// place. What has to be true is that Run returns: the process is on its way
// out, and a handle that cannot be released is released by exiting.
func TestAListenerThatWillNotCloseCannotHoldTheDaemonOpen(t *testing.T) {
	ln := newUnclosableListener(t)
	srv := &http.Server{Handler: http.NewServeMux()}
	go func() { _ = srv.Serve(ln) }()
	// Let Serve reach its accept loop, so this is the real ordering rather
	// than a race against startup.
	time.Sleep(50 * time.Millisecond)

	const grace = time.Second
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- stopServing(srv, grace) }()

	select {
	case err := <-done:
		if err == nil {
			t.Error("the stop reported success while a listener was still wedged")
		}
		// And it is bounded by the grace it was given, not by the listener.
		if elapsed := time.Since(started); elapsed > grace+stopSlack+2*time.Second {
			t.Errorf("the stop took %v with a grace of %v", elapsed, grace)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("a listener that will not close held the daemon open")
	}
}

// The ordinary path still reports what it always did: a clean stop is a nil
// error, not a timeout dressed up as one.
func TestAnOrdinaryStopStillSucceeds(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}
	go func() { _ = srv.Serve(ln) }()
	time.Sleep(50 * time.Millisecond)

	defer ln.Close()
	if err := stopServing(srv, 5*time.Second); err != nil {
		t.Errorf("stopServing on an idle server = %v, want nil", err)
	}
}
