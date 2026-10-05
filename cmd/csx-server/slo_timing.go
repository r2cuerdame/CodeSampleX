package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// withSLOTiming reports time spent inside csx-server through its first write.
// The public probe can compare this with request-to-first-byte to locate delay
// outside the application. Only fixed SLO routes are exposed, without query
// strings or any caller-supplied label.
func withSLOTiming(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz", "/version", "/v1/stats", "/v1/shards/npm/zod/3":
			start := time.Now()
			next.ServeHTTP(&sloTimingWriter{ResponseWriter: w, start: start}, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

type sloTimingWriter struct {
	http.ResponseWriter
	start time.Time
	wrote bool
}

func (w *sloTimingWriter) beforeWrite() {
	if w.wrote {
		return
	}
	w.wrote = true
	app := fmt.Sprintf("csx_app;dur=%.3f", float64(time.Since(w.start))/float64(time.Millisecond))
	if phases := w.Header().Values("Server-Timing"); len(phases) > 0 {
		app += ", " + strings.Join(phases, ", ")
	}
	w.Header().Set("Server-Timing", app)
}

func (w *sloTimingWriter) WriteHeader(code int) {
	w.beforeWrite()
	w.ResponseWriter.WriteHeader(code)
}

func (w *sloTimingWriter) Write(data []byte) (int, error) {
	w.beforeWrite()
	return w.ResponseWriter.Write(data)
}

// ResponseController can still reach the original writer if a handler needs
// an optional capability such as flushing.
func (w *sloTimingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
