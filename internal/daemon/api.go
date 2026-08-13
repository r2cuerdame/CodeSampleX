package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// StatusInfo is the GET /local/v1/status body.
type StatusInfo struct {
	SchemaVersion int    `json:"schemaVersion"`
	Version       string `json:"version"`
	Mode          string `json:"mode"`
	Home          string `json:"home"`
	PeerID        string `json:"peerId"`
	Uptime        string `json:"uptime"`
	QueueDepth    int    `json:"queueDepth"`
	LastUpload    string `json:"lastUpload,omitempty"`
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
	SampleID  string `json:"sampleId"`
	Applied   bool   `json:"applied"`
	BuildPass *bool  `json:"buildPass,omitempty"`
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
	if v, ok, _ := d.DB.GetStat(ctx, statLastUpload); ok {
		st.LastUpload = v
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
	resp := d.Engine.Search(ctx, req)
	if resp.Miss {
		d.incrStat(ctx, statMisses, 1)
	} else if len(resp.Results) > 0 {
		top := resp.Results[0]
		_ = d.DB.RecordHit(ctx, localdb.HitRow{
			TS: time.Now().UTC(), Query: req.Query,
			Grade: top.Grade, SampleID: top.SampleID,
		})
	}
	writeJSON(w, http.StatusOK, resp)
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
	hit := localdb.HitRow{
		TS:       time.Now().UTC(),
		SampleID: req.SampleID,
		Adopted:  req.Applied,
	}
	if req.BuildPass != nil {
		hit.PostBuildPass = sql.NullBool{Bool: *req.BuildPass, Valid: true}
	}
	if err := d.DB.RecordHit(ctx, hit); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = d.DB.TouchSample(ctx, req.SampleID)

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
	if err == nil {
		_, _ = d.DB.Enqueue(ctx, "adoption", string(payload))
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
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
