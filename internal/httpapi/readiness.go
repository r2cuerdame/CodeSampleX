package httpapi

import (
	"io"
	"net/http"
	"sync"
	"time"
)

const authoringHealthWindow = 15 * time.Minute

type authoringHealthBucket struct {
	second   int64
	requests uint64
	failures uint64
}

type authoringWorkHealth struct {
	mu      sync.Mutex
	buckets [int(authoringHealthWindow / time.Second)]authoringHealthBucket
}

func (h *authoringWorkHealth) record(now time.Time, status int) {
	if status >= 400 && status < 500 {
		return
	}
	second := now.Unix()
	index := (second%int64(len(h.buckets)) + int64(len(h.buckets))) % int64(len(h.buckets))
	h.mu.Lock()
	defer h.mu.Unlock()
	b := &h.buckets[index]
	if b.second != second {
		*b = authoringHealthBucket{second: second}
	}
	b.requests++
	if status >= 500 {
		b.failures++
	}
}

func (h *authoringWorkHealth) unhealthy(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	var requests, failures uint64
	second := now.Unix()
	for _, b := range h.buckets {
		if age := second - b.second; age >= 0 && age < int64(len(h.buckets)) {
			requests += b.requests
			failures += b.failures
		}
	}
	return failures >= 3 && failures*2 >= requests
}

func (a *api) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if a.workHealth.unhealthy(a.now()) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "authoring work unavailable")
		return
	}
	a.handleHealthz(w, r)
}

func (a *api) observeAuthoringWork(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		response := &authoringHealthResponse{ResponseWriter: w}
		next(response, r)
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		a.workHealth.record(a.now(), status)
	}
}

type authoringHealthResponse struct {
	http.ResponseWriter
	status int
}

func (w *authoringHealthResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *authoringHealthResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *authoringHealthResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
