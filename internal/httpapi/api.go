// Package httpapi implements the csx-server /v1 HTTP API (plan contract C5).
// All responses are JSON; errors are {"error": string}; handlers never
// panic outward. Registry and web reads only ever touch materialized
// snapshots — raw evidence is aggregated by internal/compatibility, never
// in-request (goal.md §14.5). The server never stores raw error text,
// paths, or user agents (§2.2).
package httpapi

import (
	"context"

	"encoding/json"
	"errors"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"io"
	"net/http"
	"strings"
	"time"

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
	Store serverstore.Store
	Blobs blob.Store
	Cfg   serverstore.ServerConfig
	// PublicnessChecker is an interface so the outbound-lookup behaviour is
	// testable: this dependency is the one that reaches a third-party
	// registry with a name the caller chose, and a test has to be able to
	// count that.
	Checker PublicnessChecker

	// GitHub device-flow endpoints; empty fields use the github.com
	// defaults. Tests point these at httptest servers.
	GitHubDeviceURL string
	GitHubTokenURL  string
	GitHubUserURL   string

	// HTTPClient performs outbound GitHub calls; nil means a 10s-timeout
	// client.
	HTTPClient *http.Client

	// PeerProbe dials an announcing peer back to confirm other peers could
	// actually reach it. nil uses a real HTTP ping; tests supply a stub so
	// no test ever opens a socket to the outside.
	PeerProbe func(ctx context.Context, addr string, port int) bool

	// Limits holds the per-class rate limiters; nil builds the defaults.
	// Tests that hammer an endpoint on purpose install their own.
	Limits *limiters

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
	if a.d.Limits == nil {
		a.d.Limits = newLimiters()
	}
	mux := http.NewServeMux()
	lim := a.d.Limits

	// Writes are the expensive, abusable half: they allocate disk, database
	// rows and connections. Reads are cheap and served from materialized
	// snapshots, so they get a larger budget.
	a.route(mux, "POST /v1/evidence/batches", a.limit(lim.write, a.handleEvidenceBatches))
	a.route(mux, "GET /v1/registry/packages/{purl}", a.limit(lim.read, a.handleRegistryPackage))
	a.route(mux, "GET /v1/registry/symbols/{ecosystem}/{rest...}", a.limit(lim.read, a.handleRegistrySymbol))
	a.route(mux, "POST /v1/search", a.limit(lim.read, a.handleSearch))
	a.route(mux, "GET /v1/shards/{ecosystem}/{rest...}", a.limit(lim.read, a.handleShard))
	a.route(mux, "POST /v1/samples", a.limit(lim.publish, a.handleSampleUpload))
	a.route(mux, "GET /v1/samples/{sampleId}", a.limit(lim.read, a.handleSampleMeta))
	a.route(mux, "GET /v1/samples/{sampleId}/artifact", a.limit(lim.read, a.handleSampleArtifact))
	a.route(mux, "POST /v1/wanted", a.limit(lim.write, a.handleWanted))
	a.route(mux, "POST /v1/verifications", a.limit(lim.write, a.handleVerification))
	a.route(mux, "GET /v1/verification/jobs", a.limit(lim.read, a.handleJobsList))
	a.route(mux, "POST /v1/verification/jobs/{id}/claim", a.limit(lim.write, a.handleJobClaim))
	a.route(mux, "POST /v1/peers/announce", a.limit(lim.write, a.handlePeerAnnounce))
	a.route(mux, "GET /v1/peers/for-sample/{sampleId}", a.limit(lim.read, a.handlePeersForSample))
	a.route(mux, "GET /v1/stats", a.limit(lim.read, a.handleStats))
	a.route(mux, "GET /v1/adapters", a.limit(lim.read, a.handleAdapters))
	a.route(mux, "POST /v1/auth/github/device", a.limit(lim.auth, a.handleGitHubDevice))
	a.route(mux, "POST /v1/auth/github/poll", a.limit(lim.auth, a.handleGitHubPoll))

	// healthz is what the container healthcheck and any monitor believe.
	// A hardcoded "ok" would report a healthy server with a dead database,
	// which is worse than no health check at all — so it touches the store.
	// Deliberately unlimited: throttling it would take the deployment down
	// on its own.
	mux.HandleFunc("GET /healthz", a.handleHealthz)
	return mux
}

// healthzTimeout keeps a stuck database from turning the health check into
// another hung request; the probe is a single trivial query.
const healthzTimeout = 3 * time.Second

func (a *api) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	unhealthy := func(reason string) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, reason)
	}
	if a.d.Store == nil {
		unhealthy("no store configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), healthzTimeout)
	defer cancel()
	// Any trivial read proves the pool can hand out a live connection.
	if _, _, err := a.d.Store.GetLatestStats(ctx); err != nil {
		unhealthy("database unavailable")
		return
	}
	_, _ = io.WriteString(w, "ok")
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

// PublicnessChecker answers whether a package exists on its public registry.
// *registry.Checker satisfies it.
type PublicnessChecker interface {
	Check(ctx context.Context, p domain.PURL) string
}
