// Package httpapi implements the csx-server /v1 HTTP API (plan contract C5).
// All responses are JSON; errors are {"error": string}; handlers never
// panic outward. Registry and web reads only ever touch materialized
// snapshots — raw evidence is aggregated by internal/compatibility, never
// in-request (goal.md §14.5). The server never stores raw error text,
// paths, or user agents (§2.2).
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"github.com/r2cuerdame/codesamplex/internal/storage/blob"
)

// GitHub device-flow defaults (overridable in Deps for tests).
const (
	defaultGitHubDeviceURL = "https://github.com/login/device/code"
	defaultGitHubTokenURL  = "https://github.com/login/oauth/access_token"
	defaultGitHubUserURL   = "https://api.github.com/user"
)

// Deps carries everything the API handlers need. Checker is nil in trust
// mode (CSX_PUBLIC_CHECK=trust): every syntactically valid public-ecosystem
// batch is accepted without a registry probe (dev/e2e only).
type Deps struct {
	Store   serverstore.Store
	Blobs   blob.Store
	Cfg     serverstore.ServerConfig
	Checker *registry.Checker

	// GitHub device-flow endpoints; empty fields use the github.com
	// defaults. Tests point these at httptest servers.
	GitHubDeviceURL string
	GitHubTokenURL  string
	GitHubUserURL   string

	// HTTPClient performs outbound GitHub calls; nil means a 10s-timeout
	// client.
	HTTPClient *http.Client

	// Now is a test seam; nil means time.Now.
	Now func() time.Time
}

type api struct {
	d Deps
}

// NewMux builds the /v1 API mux with every C5 route registered.
func NewMux(d Deps) *http.ServeMux {
	if d.GitHubDeviceURL == "" {
		d.GitHubDeviceURL = defaultGitHubDeviceURL
	}
	if d.GitHubTokenURL == "" {
		d.GitHubTokenURL = defaultGitHubTokenURL
	}
	if d.GitHubUserURL == "" {
		d.GitHubUserURL = defaultGitHubUserURL
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	a := &api{d: d}
	mux := http.NewServeMux()

	a.route(mux, "POST /v1/evidence/batches", a.handleEvidenceBatches)
	a.route(mux, "GET /v1/registry/packages/{purl}", a.handleRegistryPackage)
	a.route(mux, "GET /v1/registry/symbols/{ecosystem}/{rest...}", a.handleRegistrySymbol)
	a.route(mux, "POST /v1/search", a.handleSearch)
	a.route(mux, "GET /v1/shards/{ecosystem}/{rest...}", a.handleShard)
	a.route(mux, "POST /v1/samples", a.handleSampleUpload)
	a.route(mux, "GET /v1/samples/{sampleId}", a.handleSampleMeta)
	a.route(mux, "GET /v1/samples/{sampleId}/artifact", a.handleSampleArtifact)
	a.route(mux, "POST /v1/verifications", a.handleVerification)
	a.route(mux, "GET /v1/verification/jobs", a.handleJobsList)
	a.route(mux, "POST /v1/verification/jobs/{id}/claim", a.handleJobClaim)
	a.route(mux, "POST /v1/peers/announce", a.handlePeerAnnounce)
	a.route(mux, "GET /v1/peers/for-sample/{sampleId}", a.handlePeersForSample)
	a.route(mux, "GET /v1/stats", a.handleStats)
	a.route(mux, "GET /v1/adapters", a.handleAdapters)
	a.route(mux, "POST /v1/auth/github/device", a.handleGitHubDevice)
	a.route(mux, "POST /v1/auth/github/poll", a.handleGitHubPoll)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	})
	return mux
}

// route registers h with a recover guard: a handler panic becomes a JSON
// 500, never a dropped connection with a stack trace.
func (a *api) route(mux *http.ServeMux, pattern string, h http.HandlerFunc) {
	mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		h(w, r)
	})
}

func (a *api) now() time.Time {
	if a.d.Now != nil {
		return a.d.Now()
	}
	return time.Now().UTC()
}

// trustMode reports whether the publicness gate is disabled
// (CSX_PUBLIC_CHECK=trust, dev/e2e only).
func (a *api) trustMode() bool { return a.d.Cfg.PublicCheck == "trust" }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// readJSON decodes the request body into v with a hard size limit.
// It returns false after writing the error response itself.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// bearerToken extracts an optional "Authorization: Bearer x" token.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if tok, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(tok)
	}
	return ""
}
