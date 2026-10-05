package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/servertiming"
)

// timingResponse delays committing these small diagnostic responses until the
// handler has finished. That lets Server-Timing include serialization time in
// an ordinary response header rather than an HTTP trailer.
type timingResponse struct {
	http.ResponseWriter
	ctx    context.Context
	status int
	body   bytes.Buffer
}

func (w *timingResponse) WriteHeader(status int) {
	if w.status == 0 {
		servertiming.Serialization(w.ctx)
		w.status = status
	}
}

func (w *timingResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		servertiming.Serialization(w.ctx)
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *timingResponse) WriteString(s string) (int, error) {
	if w.status == 0 {
		servertiming.Serialization(w.ctx)
		w.status = http.StatusOK
	}
	return w.body.WriteString(s)
}

func (a *api) timed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, timing := servertiming.Ensure(r.Context())
		buffered := &timingResponse{ResponseWriter: w, ctx: ctx}
		h(buffered, r.WithContext(ctx))
		middleware, poolWait, work, serialization := timing.Durations()
		w.Header().Set("Server-Timing", fmt.Sprintf("middleware;dur=%.3f, db_wait;dur=%.3f, query_handler;dur=%.3f, serialize;dur=%.3f",
			milliseconds(middleware), milliseconds(poolWait), milliseconds(work), milliseconds(serialization)))
		if buffered.status == 0 {
			buffered.status = http.StatusOK
		}
		w.WriteHeader(buffered.status)
		_, _ = buffered.body.WriteTo(w)
	}
}

func (a *api) timedHandler(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		servertiming.Handler(r.Context())
		h(w, r)
	}
}

func milliseconds(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
