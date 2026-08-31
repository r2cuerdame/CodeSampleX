package daemon

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// A handler still running when the grace period expires does not get to keep
// its connection.
//
// srv.Shutdown closes the listeners, waits for in-flight requests, and at its
// deadline gives up and returns — leaving every connection it was waiting on
// still running, owned by nothing that will ever end it. So the grace period
// bounded how long the caller WAITED, not how long the server LIVED.
//
// This is NOT the Windows release flake in #130. That one is not reproduced
// and nothing here claims to fix it; this is a gap found while reading that
// path, and it is the difference between a stop and a stop that took.
//
// Two earlier versions of this test drove a real daemon and passed with the
// fix removed, because Run returns at the deadline either way and neither
// version ever got Shutdown to actually time out. What separates the two
// behaviours is not timing but whether the stranded connection is closed, and
// producing a connection Shutdown will give up on needs a handler the test
// controls — which is why stopServer takes a plain *http.Server.
func TestAStuckHandlerDoesNotKeepItsConnection(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/stuck", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { close(release) })

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET /stuck HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler never ran, so nothing was in flight to strand")
	}

	grace := 200 * time.Millisecond
	start := time.Now()
	if err := stopServer(srv, grace); err == nil {
		t.Fatal("Shutdown reported success with a handler still running; " +
			"this test is not exercising the timeout it exists for")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("stopServer took %v, grace was %v", elapsed, grace)
	}

	// The assertion that separates the two behaviours. Without Close this
	// socket stays open, serving a request nobody is waiting for, until
	// ReadHeaderTimeout happens to reap it.
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	switch _, err := conn.Read(make([]byte, 1)); {
	case err == nil:
		t.Error("the connection was still being served after stopServer returned")
	case isTimeout(err):
		t.Error("the connection was neither closed nor served after stopServer returned; " +
			"nothing owns it and only ReadHeaderTimeout will end it")
	}
}

// A server with nothing in flight stops inside the grace period and reports no
// error, so the Close path is reached only when draining actually failed.
func TestAnIdleServerStopsCleanly(t *testing.T) {
	srv := &http.Server{Handler: http.NewServeMux(), ReadHeaderTimeout: time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	if err := stopServer(srv, 5*time.Second); err != nil {
		t.Errorf("stopServer on an idle server = %v, want nil", err)
	}
}

func isTimeout(err error) bool {
	ne, ok := err.(net.Error)
	return ok && ne.Timeout()
}
