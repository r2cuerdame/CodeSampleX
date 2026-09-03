package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// StatusInfo is the GET /local/v1/status body.
type StatusInfo struct {
	SchemaVersion     int    `json:"schemaVersion"`
	Version           string `json:"version"`
	Mode              string `json:"mode"`
	Home              string `json:"home"`
	PeerID            string `json:"peerId"`
	Uptime            string `json:"uptime"`
	QueueDepth        int    `json:"queueDepth"`
	LastUpload        string `json:"lastUpload,omitempty"`
	LastUploadAttempt string `json:"lastUploadAttempt,omitempty"`
	LastUploadError   string `json:"lastUploadError,omitempty"`
	// Sync is present only while a sync is running: which stage, how far
	// through it, and when it began. A client waiting on POST /local/v1/sync
	// polls this to say something during the minutes it takes.
	Sync *SyncProgress `json:"sync,omitempty"`
}

// SyncProgress is where a running sync is.
type SyncProgress struct {
	Stage     string    `json:"stage"`
	Done      int       `json:"done"`
	Total     int       `json:"total"`
	StartedAt time.Time `json:"startedAt"`
}

// SampleInfo is the GET /local/v1/samples/{id} body: the localdb row plus
// its parsed manifest.
type SampleInfo struct {
	SchemaVersion int                   `json:"schemaVersion"`
	SampleID      string                `json:"sampleId"`
	CaseID        string                `json:"caseId,omitempty"`
	Status        string                `json:"status"`
	License       string                `json:"license,omitempty"`
	OriginSeeder  string                `json:"originSeeder,omitempty"`
	CreatedAt     string                `json:"createdAt,omitempty"`
	Pinned        bool                  `json:"pinned"`
	HasArtifact   bool                  `json:"hasArtifact"`
	Manifest      domain.SampleManifest `json:"manifest"`
}

// AdoptionRequest is the POST /local/v1/adoption body (MCP
// report_sample_adoption lands here too).
type AdoptionRequest struct {
	OfferID   string `json:"offerId"`
	SampleID  string `json:"sampleId"`
	Applied   bool   `json:"applied"`
	BuildPass *bool  `json:"buildPass,omitempty"`
}

// LocalSearchResponse extends a public search response with the opaque
// capability needed to report on this exact local offer. Keeping the field
// here prevents it from entering domain.SearchResponse, the public v1 schema,
// shard documents, or upload payloads.
type LocalSearchResponse struct {
	domain.SearchResponse
	OfferID string `json:"offerId,omitempty"`
}

// adoptionEvidence is the queued ADOPTION_EVIDENCE payload. By
// construction it carries only the rotating anon ID, the daily epoch, the
// content-addressed sample id, and two booleans — nothing else may ever
// be added without a privacy review (goal.md §2.2).
type adoptionEvidence struct {
	SchemaVersion int    `json:"schemaVersion"`
	EvidenceClass string `json:"evidenceClass"` // ADOPTION_EVIDENCE
	Epoch         string `json:"epoch"`
	AnonID        string `json:"anonId"`
	SampleID      string `json:"sampleId"`
	Applied       bool   `json:"applied"`
	BuildPass     *bool  `json:"buildPass,omitempty"`
}

// QueuePreview is the GET /local/v1/queue body — the §12.5 privacy
// preview. Batches is EXACTLY the "batches" array a community-mode upload
// would POST to /v1/evidence/batches; Queued lists other pending upload
// payloads verbatim.
type QueuePreview struct {
	SchemaVersion int                       `json:"schemaVersion"`
	Batches       []domain.ObservationBatch `json:"batches"`
	Queued        []QueuedUpload            `json:"queued"`
}

// QueuedUpload is one non-batch pending upload with its exact payload.
type QueuedUpload struct {
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"createdAt,omitempty"`
}

// Handler returns the daemon mux served on both listeners.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /local/v1/status", d.handleStatus)
	mux.HandleFunc("POST /local/v1/search", d.handleSearch)
	mux.HandleFunc("GET /local/v1/samples/{id}", d.handleSample)
	mux.HandleFunc("POST /local/v1/adoption", d.handleAdoption)
	mux.HandleFunc("GET /local/v1/queue", d.handleQueue)
	mux.HandleFunc("POST /local/v1/sync", d.handleSync)
	mux.HandleFunc("GET /local/v1/stats", d.handleStats)
	mux.HandleFunc("POST /local/v1/shutdown", d.handleShutdown)
	mux.HandleFunc("GET /ui", d.handleUI)
	mux.HandleFunc("GET /ui/", d.handleUI)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v) //nolint:errcheck // client went away
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	st := StatusInfo{
		SchemaVersion: 1,
		Version:       Version,
		Mode:          d.Cfg.Mode,
		Home:          d.Home,
		PeerID:        d.Ident.PeerID(),
		Uptime:        d.uptime(),
	}
	st.QueueDepth, _ = d.queueDepth(ctx)
	st.Sync = d.syncProgress()
	if v, ok, _ := d.DB.GetStat(ctx, statLastUpload); ok {
		st.LastUpload = v
	}
	if v, ok, _ := d.DB.GetStat(ctx, statLastUploadAttempt); ok {
		st.LastUploadAttempt = v
	}
	if v, ok, _ := d.DB.GetStat(ctx, statLastUploadError); ok {
		st.LastUploadError = v
	}
	writeJSON(w, http.StatusOK, st)
}

func (d *Daemon) uptime() string {
	if d.start.IsZero() {
		return "0s"
	}
	return time.Since(d.start).Round(time.Second).String()
}

// queueDepth counts pending observation aggregates plus queued uploads.
func (d *Daemon) queueDepth(ctx context.Context) (int, error) {
	rows, err := d.DB.PendingObservations(ctx, 1000)
	if err != nil {
		return 0, err
	}
	items, err := d.DB.QueuePending(ctx, 1000)
	if err != nil {
		return len(rows), err
	}
	return len(rows) + len(items), nil
}

func (d *Daemon) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req domain.SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid search request: "+err.Error())
		return
	}
	resp := d.SearchAndRecord(ctx, req)
	writeJSON(w, http.StatusOK, resp)
}

// normalizeLocalSearchRequest is the only legacy-softening boundary. Before
// v2, csx CLI put scanner-derived symbols in symbols and had no provenance;
// public/MCP inputs do not pass through this endpoint and remain fail-closed.
// New CLIs retain a symbols compatibility copy for old daemons, then mark it
// context so a new daemon can remove the exclusion again.
func normalizeLocalSearchRequest(req *domain.SearchRequest) {
	if req == nil {
		return
	}
	legacyCLI := req.SchemaVersion < 2 && req.SymbolProvenance == "" && len(req.Symbols) > 0
	contextCopy := req.SymbolProvenance == domain.SearchProvenanceContext
	if legacyCLI || contextCopy {
		seen := make(map[string]bool, len(req.ContextSymbols)+len(req.Symbols))
		merged := make([]string, 0, len(req.ContextSymbols)+len(req.Symbols))
		for _, symbols := range [][]string{req.ContextSymbols, req.Symbols} {
			for _, symbol := range symbols {
				key := strings.ToLower(strings.TrimSpace(symbol))
				if key == "" || seen[key] {
					continue
				}
				seen[key] = true
				merged = append(merged, symbol)
			}
		}
		req.ContextSymbols = merged
		req.Symbols = nil
		req.SymbolProvenance = domain.SearchProvenanceContext
	}
	if legacyCLI && req.EnvironmentProvenance == "" {
		req.EnvironmentProvenance = domain.SearchProvenanceContext
	}
}

func (d *Daemon) handleSample(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	row, found, err := d.DB.GetSample(ctx, id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "sample not found: "+id)
		return
	}
	info := SampleInfo{
		SchemaVersion: 1,
		SampleID:      row.SampleID,
		CaseID:        row.CaseID,
		Status:        row.Status,
		License:       row.License,
		OriginSeeder:  row.OriginSeeder,
		Pinned:        row.Pinned,
		HasArtifact:   row.HasArtifact,
	}
	if !row.CreatedAt.IsZero() {
		info.CreatedAt = row.CreatedAt.UTC().Format(time.RFC3339)
	}
	if err := json.Unmarshal([]byte(row.ManifestJSON), &info.Manifest); err != nil {
		writeErr(w, http.StatusInternalServerError, "corrupt manifest for "+id)
		return
	}
	_ = d.DB.TouchSample(ctx, id)
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleAdoption(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req AdoptionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid adoption report: "+err.Error())
		return
	}
	if req.SampleID == "" {
		writeErr(w, http.StatusBadRequest, "sampleId required")
		return
	}
	if req.OfferID == "" {
		writeErr(w, http.StatusBadRequest, localdb.ErrOfferIDRequired.Error())
		return
	}
	var pass sql.NullBool
	if req.BuildPass != nil {
		pass = sql.NullBool{Bool: *req.BuildPass, Valid: true}
	}
	outboxPayload := ""
	if d.Cfg != nil && d.Cfg.Mode == config.ModeCommunity {
		epoch := time.Now().UTC().Format("2006-01-02")
		payload, err := json.Marshal(adoptionEvidence{
			SchemaVersion: 1,
			EvidenceClass: string(domain.ClassAdoptionEvidence),
			Epoch:         epoch,
			AnonID:        d.Ident.AnonID(epoch),
			SampleID:      req.SampleID,
			Applied:       req.Applied,
			BuildPass:     req.BuildPass,
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		outboxPayload = string(payload)
	}
	// Only a local search offer can be adopted. Without that correlation an
	// arbitrary sample id says nothing about what CodeSampleX showed, so it
	// must not become either a journey row or upload evidence. In community
	// mode the update and outbox insert are one transaction, so an enqueue
	// failure leaves the offer token retryable and is returned to the caller.
	outcome, err := d.DB.CorrelateInterventionAdoption(ctx, req.OfferID, req.SampleID, req.Applied, pass, outboxPayload)
	if errors.Is(err, localdb.ErrNoEligibleIntervention) {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = d.DB.TouchSample(ctx, req.SampleID)
	writeJSON(w, http.StatusOK, map[string]any{
		"recorded":               true,
		"uploadQueued":           outcome.UploadQueued,
		"reportedFailureAvoided": outcome.ReportedFailureAvoided(),
	})
}

func (d *Daemon) handleQueue(w http.ResponseWriter, r *http.Request) {
	preview, err := d.queuePreview(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

// queuePreview builds the privacy preview through the REAL batcher code
// path, so what the user sees is byte-for-byte what an upload would send.
// Drain marks rows uploaded; the preview immediately flips them back to
// pending (a zero-count re-record touches only the uploaded flag), all
// under batchMu so a concurrent upload cannot interleave.
func (d *Daemon) queuePreview(ctx context.Context) (*QueuePreview, error) {
	d.batchMu.Lock()
	defer d.batchMu.Unlock()

	batches, err := d.Batcher.Drain(ctx)
	if err != nil {
		return nil, err
	}
	var restoreErr error
	for _, b := range batches {
		key := localdb.ObsKey{
			Epoch:            b.Epoch,
			PURL:             b.Package,
			Symbol:           b.Symbol,
			SymbolConfidence: b.SymbolConfidence,
			EnvHash:          b.Environment.Hash(),
			Stage:            b.Stage,
			Result:           b.Result,
			ErrorFP:          b.ErrorFingerprint,
			ErrorCode:        b.ErrorCode,
		}
		if err := d.DB.RecordObservation(ctx, key, 0); err != nil && restoreErr == nil {
			restoreErr = err
		}
	}
	if restoreErr != nil {
		return nil, restoreErr
	}
	if batches == nil {
		batches = []domain.ObservationBatch{}
	}

	items, err := d.DB.QueuePending(ctx, 200)
	if err != nil {
		return nil, err
	}
	queued := make([]QueuedUpload, 0, len(items))
	for _, it := range items {
		q := QueuedUpload{Kind: it.Kind, Payload: json.RawMessage(it.Payload)}
		if !json.Valid([]byte(it.Payload)) {
			raw, _ := json.Marshal(it.Payload)
			q.Payload = raw
		}
		if !it.CreatedAt.IsZero() {
			q.CreatedAt = it.CreatedAt.UTC().Format(time.RFC3339)
		}
		queued = append(queued, q)
	}
	return &QueuePreview{SchemaVersion: 1, Batches: batches, Queued: queued}, nil
}

func (d *Daemon) handleSync(w http.ResponseWriter, r *http.Request) {
	res := d.SyncNow(r.Context())
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := d.StatsNow(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"stopping": true})
	d.requestShutdown()
}

// SearchAndRecord runs a search and writes the local hit or miss behind
// csx stats, csx ui and get_local_stats.
//
// It exists because the recording lived only inside this HTTP handler, so
// every other caller of Engine.Search silently counted nothing: the MCP
// tools, and the CLI whenever the daemon was down. Those counters are shown
// to the user as fact and read 0 forever. One entry point is harder to
// forget than a convention.
//
// Queries never leave the machine — the hits table is local and is never
// uploaded — and a recording failure must not break a search.
func (d *Daemon) SearchAndRecord(ctx context.Context, req domain.SearchRequest) LocalSearchResponse {
	normalizeLocalSearchRequest(&req)
	resp := d.Engine.Search(ctx, req)
	// A miss for a package this machine never synced describes the local
	// cache, not the network. Fetch the named packages' shards once and
	// ask again — community mode only, because naming a package to the
	// server is exactly what local-only exists to avoid.
	if resp.Miss && search.FetchMissing(ctx, *d.Engine, d.Syncer, d.Cfg.Mode, req) {
		resp = d.Engine.Search(ctx, req)
	}
	// Search retrieves candidates; this boundary decides which ones are safe
	// to present and therefore safe to record as offers. MCP already applies
	// this gate before its two-phase record path. Keeping the daemon path on
	// the same contract prevents an unrelated nearest neighbour from becoming
	// a local HIT merely because the CLI happened to reach a running daemon.
	beforeGate := len(resp.Results)
	resp, suppressed := domain.GateNormalOutput(req, resp, nil)
	domain.RecordOutputGateDiagnostic(&resp, req, beforeGate, suppressed)
	if resp.Miss || len(resp.Results) == 0 {
		d.incrStat(ctx, statMisses, 1)
		// A miss is a demand signal. Queued rather than posted: the queue
		// retries, works offline, and a search never waits on it.
		evidence.QueueWanted(ctx, d.DB, d.Ident, d.Cfg, req)
		return LocalSearchResponse{SearchResponse: resp}
	}
	top := resp.Results[0]
	now := time.Now().UTC()
	offerID, _ := d.DB.RecordSearchOffer(ctx, localdb.HitRow{
		TS: now, Query: req.Query,
		Grade: top.Grade, SampleID: top.SampleID,
	}, localdb.InterventionRow{
		TS:                  now,
		SampleID:            top.SampleID,
		ExactFailureMatched: top.ExactFailureMatched,
		VerifiedOffer:       top.VerifiedOffer(),
	})
	return LocalSearchResponse{SearchResponse: resp, OfferID: offerID}
}
