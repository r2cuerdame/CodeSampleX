package httpapi

// This is a file comment, deliberately below the package clause: package
// httpapi's doc comment lives in api.go, and a second one here would only
// make "go doc httpapi" ambiguous about which text it should print.
//
// GET /v1/ops/pool-metrics (CSX-454): the machine-readable counterpart to
// the /admin dashboard's pool panel, plus the host CPU steal classifier
// (internal/hostpressure) and Farm ingest lag (CSX-453, Task 4), all under
// one JSON response. Task 6's governor and Task 7's observation-script
// wiring both read this shape by field name -- see docs/operations.md for
// the locked contract; do not rename a field here without updating both.
//
// It is registered behind the same admin authentication the /admin
// dashboard uses (cmd/csx-server/mux.go, admin.AdminAuth) -- pool pressure
// and capacity are operator information, not public.
//
// That authentication is NOT free of the database, and the difference
// matters for this route in particular. A Bearer operator token is resolved
// by admin.handler.authorizedByToken through
// serverstore.PG.ResolveAdminToken, which is an UPDATE ... RETURNING
// (it stamps last_used_at/last_used_ip), and it runs before this handler is
// entered, in whatever query class the route carries. So under the exact
// interactive-pool saturation this endpoint exists to report, the auth
// acquire can be refused with ErrPoolBusy, and the middleware answers 401 --
// the same status a wrong token gets. Reclassifying the route, or teaching
// the middleware to answer 503 for a refused pool rather than 401, is
// deferred to its own issue with #455; docs/operations.md's runbook tells an
// operator how to tell the two apart in the meantime.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/hostpressure"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// PoolStatsSource is the narrow seam onto the store's own connection pool
// counters -- the exact shape internal/compatibility/builder.go's
// interactivePressureOf and admin.PoolStatsReader already use. A store that
// satisfies either of those already satisfies this one; there is no third
// interface to keep in sync, only the same method reused.
type PoolStatsSource interface {
	PoolStats() serverstore.PoolStats
}

// FarmIngestSource is the narrow seam onto "when did evidence last land"
// (CSX-453, Task 4's LastFarmIngestAt), reused unmodified rather than
// re-derived.
type FarmIngestSource interface {
	LastFarmIngestAt(ctx context.Context) (at time.Time, found bool, err error)
}

// HostPressureReader is the narrow seam onto hostpressure.Sampler's one
// method this handler needs, so opsmetrics_test.go can exercise the handler
// with a stub that never touches /proc. A live *hostpressure.Sampler
// satisfies this interface directly -- production wiring in
// cmd/csx-server/mux.go passes one straight through.
type HostPressureReader interface {
	Sample() (hostpressure.Reading, error)
}

// opsMetricsReadTimeout bounds the one PostgreSQL read this HANDLER makes
// (LastFarmIngestAt, a single indexed aggregate) so a slow database cannot
// hang an operator's poll.
//
// It is not the only database work a request to this route does: the admin
// authentication in front of it resolves a Bearer operator token with an
// UPDATE ... RETURNING against admin_tokens (see the file comment above), and
// that call has already run, under its own budget, by the time this handler
// is entered. Everything the handler itself computes apart from
// LastFarmIngestAt is in-memory.
const opsMetricsReadTimeout = 3 * time.Second

// OpsMetricsHandler serves GET /v1/ops/pool-metrics. Its dependencies are
// deliberately narrow: a pool counters source, a Farm ingest source, and a
// host-pressure sampler -- nothing else, so it stays a JSON view over three
// existing signals rather than a grab-bag.
type OpsMetricsHandler struct {
	Pool       PoolStatsSource
	FarmIngest FarmIngestSource
	Host       HostPressureReader
}

func (h *OpsMetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := opsMetricsResponse{}

	if h.Pool != nil {
		stats := h.Pool.PoolStats()
		resp.Pool = opsPoolStats{
			Enabled:  stats.Enabled,
			MaxConns: stats.MaxConns,
			Open:     stats.Open,
			InUse:    stats.InUse,
			Idle:     stats.Idle,
			Classes:  make([]opsPoolClass, 0, len(stats.Classes)),
		}
		for _, c := range stats.Classes {
			resp.Pool.Classes = append(resp.Pool.Classes, opsPoolClass{
				Class:      c.Class,
				Limit:      c.Limit,
				InUse:      c.InUse,
				Waited:     c.Waited,
				Busy:       c.Busy,
				Timeouts:   c.Timeouts,
				Retries:    c.Retries,
				Suppressed: c.Suppressed,
			})
		}
	} else {
		resp.Pool.Classes = []opsPoolClass{}
	}

	if h.Host != nil {
		if reading, err := h.Host.Sample(); err != nil {
			// A sampling error (unsupported platform, unreadable /proc) is
			// reported, not swallowed into a zero-valued reading that would
			// look exactly like a real "0% steal" measurement -- a consumer
			// (Task 6's governor) must be able to tell "no signal" apart
			// from "measured healthy".
			resp.Host.Error = err.Error()
		} else {
			resp.Host.StealPercent = reading.StealPercent
			resp.Host.LoadAvg1 = reading.LoadAvg1
			resp.Host.SampledAt = formatOpsTime(reading.SampledAt)
		}
	} else {
		resp.Host.Error = "host pressure sampler not configured"
	}

	if h.FarmIngest != nil {
		ctx, cancel := context.WithTimeout(r.Context(), opsMetricsReadTimeout)
		at, found, err := h.FarmIngest.LastFarmIngestAt(ctx)
		cancel()
		if err == nil && found {
			resp.FarmIngest.LastCommitAt = formatOpsTime(at)
			resp.FarmIngest.LastCommitFound = true
		}
	}

	writeOpsMetricsJSON(w, http.StatusOK, resp)
}

// opsMetricsResponse is the locked JSON contract. Field names (and their
// JSON tags) must not change without updating Task 6's governor and Task
// 7's observation script, which both read this shape by name.
type opsMetricsResponse struct {
	Pool       opsPoolStats   `json:"pool"`
	Host       opsHostReading `json:"host"`
	FarmIngest opsFarmIngest  `json:"farmIngest"`
}

type opsPoolStats struct {
	Enabled  bool           `json:"enabled"`
	MaxConns int            `json:"maxConns"`
	Open     int            `json:"open"`
	InUse    int            `json:"inUse"`
	Idle     int            `json:"idle"`
	Classes  []opsPoolClass `json:"classes"`
}

// opsPoolClass reuses serverstore.ClassPoolStats's exact field names,
// lowercased for JSON, per the brief -- do not rename them.
type opsPoolClass struct {
	Class      string `json:"class"`
	Limit      int    `json:"limit"`
	InUse      int    `json:"inUse"`
	Waited     uint64 `json:"waited"`
	Busy       uint64 `json:"busy"`
	Timeouts   uint64 `json:"timeouts"`
	Retries    uint64 `json:"retries"`
	Suppressed uint64 `json:"suppressed"`
}

type opsHostReading struct {
	StealPercent float64 `json:"stealPercent"`
	LoadAvg1     float64 `json:"loadAvg1"`
	SampledAt    string  `json:"sampledAt,omitempty"`
	// Error is set instead of StealPercent/LoadAvg1 being trusted when the
	// sampler could not produce a reading (unsupported platform, unreadable
	// /proc). Empty means the reading above is real.
	Error string `json:"error,omitempty"`
}

type opsFarmIngest struct {
	LastCommitAt    string `json:"lastCommitAt,omitempty"`
	LastCommitFound bool   `json:"lastCommitFound"`
}

func formatOpsTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func writeOpsMetricsJSON(w http.ResponseWriter, status int, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		http.Error(w, "response unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
