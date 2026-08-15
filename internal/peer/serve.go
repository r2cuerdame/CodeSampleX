package peer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Handler returns the peer HTTP surface (plan P9.1):
//
//	GET /peer/v1/ping          → 200 "csx-peer" (liveness / protocol probe)
//	GET /peer/v1/samples/{id}  → artifact bytes from the CAS, 404 if absent
//
// It serves ONLY content-addressed artifacts already public on the
// network; it never exposes local paths, the DB, or anything else.
func (n *Node) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /peer/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "csx-peer")
	})
	mux.HandleFunc("GET /peer/v1/samples/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !validSampleID(id) {
			http.NotFound(w, r)
			return
		}
		// Defence in depth: even with a correct id, only a PUBLISHED
		// sample is served. The announce loop no longer offers drafts, and
		// this makes a leaked or guessed id useless against one too.
		if n.DB != nil {
			row, ok, derr := n.DB.GetSample(r.Context(), id)
			if derr != nil || !ok || !publishedStatus(row.Status) {
				http.NotFound(w, r)
				return
			}
		}
		rc, err := n.CAS.Get(id)
		if err != nil {
			// Missing and malformed ids alike are a plain 404: peers learn
			// nothing about this machine from probing.
			http.NotFound(w, r)
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", "application/gzip")
		io.Copy(w, rc)
	})
	return mux
}

// listenAddr is the address ListenAndServe binds. Split out so the choice
// of interface is testable without opening a socket.
func (n *Node) listenAddr() string {
	return fmt.Sprintf("%s:%d", n.BindAddr, n.Port)
}

// ListenAndServe serves the peer handler on n.Port until ctx is canceled,
// then shuts down gracefully. A canceled ctx returns nil; listener errors
// (port in use, …) are returned as-is.
func (n *Node) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              n.listenAddr(),
		Handler:           n.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		<-errCh // always http.ErrServerClosed after Shutdown
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// validSampleID reports whether id is a well-formed content id
// ("sha256:" + 64 lowercase hex). Everything else is rejected before it
// can reach the CAS or be interpolated into URLs.
func validSampleID(id string) bool {
	const prefix = "sha256:"
	if len(id) != len(prefix)+64 || id[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(id); i++ {
		c := id[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
