package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/anonymousclient"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const anonymousCookie = "csx_anonymous"

// Bound detached analytics writes so a slow store can undercount telemetry,
// but can never grow goroutines without limit or delay a product response.
var anonymousWriteSlots = make(chan struct{}, 32)

func validAnonymousID(id string) bool {
	return len(id) == 64 && strings.Trim(id, "0123456789abcdef") == ""
}

func anonymousRoute(r *http.Request) bool {
	if r.Method == http.MethodHead || r.Method == http.MethodOptions || r.Header.Get("Authorization") != "" {
		return false
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") && !strings.HasPrefix(r.URL.Path, "/v2/") {
		return false
	}
	for _, prefix := range []string{"/v1/auth/", "/v1/authoring/", "/v1/verification", "/v1/peers/", "/v1/presence"} {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return false
		}
	}
	switch r.Header.Get(anonymousclient.ClassHeader) {
	case "", "ordinary", "external":
		return true
	default:
		return false
	}
}

// anonymous wraps only registered routes. Successful product API requests
// (including conditional 304 reads) count; invalid paths, failures, auth,
// fleet polling, presence heartbeats and operator traffic do not.
// The ID grants no authority. IP is used only by the existing abuse limiter.
func (a *api) anonymous(h http.HandlerFunc) http.HandlerFunc {
	store, ok := a.d.Store.(serverstore.AnonymousAnalyticsStore)
	if !ok {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !anonymousRoute(r) {
			h(w, r)
			return
		}
		id := r.Header.Get(anonymousclient.Header)
		credentialPresent := validAnonymousID(id)
		if id == "" {
			if cookie, err := r.Cookie(anonymousCookie); err == nil {
				id = cookie.Value
			}
		}
		if !validAnonymousID(id) {
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				h(w, r)
				return
			}
			id = hex.EncodeToString(raw[:])
		}
		// Only header-less API callers need a cookie. Never vary public caches
		// by user identity: credential-bearing responses must not be shared.
		w.Header().Set(anonymousclient.Header, id)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Add("Vary", anonymousclient.Header)
		w.Header().Add("Vary", "Cookie")
		if r.Header.Get(anonymousclient.Header) == "" {
			http.SetCookie(w, &http.Cookie{Name: anonymousCookie, Value: id, Path: "/", HttpOnly: true, Secure: r.TLS != nil || strings.HasPrefix(a.d.Cfg.PublicURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 60 * 60})
		}
		rec := &anonymousResponse{ResponseWriter: w}
		h(rec, r)
		if rec.status == 0 || (rec.status >= 200 && rec.status < 300) || rec.status == http.StatusNotModified {
			hash := sha256.Sum256([]byte("csx-anonymous-v1|" + id))
			a.recordAnonymous(store, hex.EncodeToString(hash[:]), credentialPresent)
		}
	}
}

func (a *api) recordAnonymous(store serverstore.AnonymousAnalyticsStore, hash string, credentialPresent bool) {
	select {
	case anonymousWriteSlots <- struct{}{}:
		now := a.now()
		go func() {
			defer func() { <-anonymousWriteSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			// Analytics never inherits the request's interactive query budget.
			ctx = serverstore.WithQueryClass(ctx, serverstore.ClassBackground)
			if err := store.RecordAnonymousClient(ctx, hash, now, credentialPresent); err != nil {
				// Fixed message: never log a credential, hash, IP or request URL.
				log.Print("csx: anonymous analytics write unavailable (activity undercounted)")
			}
		}()
	default:
		// Fixed message: saturation drops telemetry instead of blocking access.
		log.Print("csx: anonymous analytics write unavailable (activity undercounted)")
	}
}

// Handlers may set public caching headers; enforce privacy at commitment.
type anonymousResponse struct {
	http.ResponseWriter
	status int
}

func (w *anonymousResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	if status >= 500 {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	} else {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *anonymousResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *anonymousResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }
