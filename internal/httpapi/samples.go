package httpapi

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Artifact limits — contract C13, enforced here INLINE (a deliberate
// duplicate of the client-side samples package: the server never trusts
// clients to have run the checks).
const (
	maxArtifactBytes  = 256 * 1024
	maxArtifactFiles  = 200
	maxSampleReqBytes = 2 << 20
)

// permissiveLicenses are the sample licenses the pool accepts (§7.5;
// MIT-0 is the default).
var permissiveLicenses = map[string]bool{
	"MIT-0": true, "MIT": true, "Apache-2.0": true, "BSD-2-Clause": true,
	"BSD-3-Clause": true, "ISC": true, "Unlicense": true, "CC0-1.0": true,
}

// forbiddenDirs inside artifacts (dependency trees, VCS state, local envs
// never belong in a clean-room sample).
var forbiddenDirs = map[string]bool{
	"node_modules": true, ".git": true, "venv": true, "target": true,
}

// blobBudgetExceeded reports whether stored artifacts have reached the
// configured budget, and how much is in use. A budget of 0 means no limit.
// An unreadable blob directory is NOT treated as full: refusing uploads
// because a stat failed would turn a transient error into an outage.
func (a *api) blobBudgetExceeded(ctx context.Context) (bool, int64) {
	budget := a.d.Cfg.BlobBudgetBytes
	if budget <= 0 {
		return false, 0
	}
	used, err := a.d.Blobs.TotalSize(ctx)
	if err != nil {
		return false, 0
	}
	return used >= budget, used
}

// handleSampleUpload implements POST /v1/samples (multipart: manifest JSON,
// sampleId, artifact tar.gz). The sample id MUST be the sha256 of the exact
// artifact bytes; static checks run before anything is stored.
func (a *api) handleSampleUpload(w http.ResponseWriter, r *http.Request) {
	if a.d.Blobs == nil {
		writeErr(w, http.StatusServiceUnavailable, "sample storage not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSampleReqBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		writeErr(w, http.StatusBadRequest, "invalid multipart form: "+err.Error())
		return
	}

	manifestJSON := r.FormValue("manifest")
	claimedID := r.FormValue("sampleId")
	if manifestJSON == "" || claimedID == "" {
		writeErr(w, http.StatusBadRequest, "manifest and sampleId form fields are required")
		return
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid manifest JSON: "+err.Error())
		return
	}
	if manifest.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "manifest schemaVersion must be 1")
		return
	}
	if len(manifest.ContractCommand) == 0 || manifest.VerifierAdapter == "" {
		writeErr(w, http.StatusBadRequest, "manifest requires contractCommand and verifierAdapter")
		return
	}

	license := manifest.License
	if license == "" {
		license = "MIT-0"
	}
	if !permissiveLicenses[license] {
		writeErr(w, http.StatusBadRequest, "license "+license+" is not a permitted permissive license")
		return
	}

	file, _, err := r.FormFile("artifact")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "artifact file field is required")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxArtifactBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "reading artifact failed")
		return
	}
	if len(data) > maxArtifactBytes {
		writeErr(w, http.StatusBadRequest, "artifact exceeds 256KB limit")
		return
	}

	// sampleId = SHA-256 of the exact artifact bytes — recomputed, never
	// trusted (§7.5).
	if got := domain.SHA256Hex(data); got != claimedID {
		writeErr(w, http.StatusBadRequest, "sampleId does not match artifact hash")
		return
	}

	if err := checkArtifactStatic(data, manifest); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Optional seeder attribution via API token; anonymous is allowed.
	seeder := "anonymous"
	if tok := bearerToken(r); tok != "" {
		id, ok, err := a.d.Store.IdentityByAPIToken(r.Context(), sha256Hex(tok))
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "identity lookup failed")
			return
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "invalid api token")
			return
		}
		seeder = id.Login
	}

	ctx := r.Context()
	cse := manifest.Case
	if cse.CaseID == "" {
		cse.CaseID = cse.ComputeID()
	}
	if err := a.d.Store.SaveCase(ctx, cse); err != nil {
		writeErr(w, http.StatusInternalServerError, "saving case failed")
		return
	}
	// Sample upload is anonymous by design, so the blob directory is the
	// one place an unauthenticated caller can consume disk without limit.
	// Filling it takes PostgreSQL down with it — the two share the volume —
	// so refuse new artifacts past the budget instead. Already-stored
	// content is exempt: a re-upload is a no-op in a content-addressed
	// store and must not fail once the budget is reached.
	if have, herr := a.d.Blobs.Has(ctx, claimedID); herr == nil && !have {
		if full, used := a.blobBudgetExceeded(ctx); full {
			writeErr(w, http.StatusInsufficientStorage,
				"sample storage is at its configured budget ("+
					strconv.FormatInt(used>>20, 10)+"MB); no new artifacts are accepted")
			return
		}
	}

	blobID, err := a.d.Blobs.Put(ctx, bytes.NewReader(data))
	if err != nil || blobID != claimedID {
		writeErr(w, http.StatusInternalServerError, "storing artifact failed")
		return
	}
	manifest.License = license
	if err := a.d.Store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     claimedID,
		CaseID:       cse.CaseID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "PUBLISHED",
		OriginSeeder: seeder,
		License:      license,
		SizeBytes:    int64(len(data)),
		CreatedAt:    a.now(),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "saving sample failed")
		return
	}
	// Queue cross verification: a peer other than the origin must confirm
	// the contract before the sample earns CROSS_PASS (§10.1). Re-publishing
	// the same content must not stack duplicate work on peers — one open
	// cross job per sample is enough, and none once it is already crossed.
	if err := a.queueCrossVerification(ctx, claimedID); err != nil {
		writeErr(w, http.StatusInternalServerError, "queueing verification failed")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"sampleId": claimedID,
		"status":   "PUBLISHED",
	})
}

// queueCrossVerification adds one open cross job for a sample unless it
// already has outstanding work or has already been cross-verified.
// Without this, every re-publish of identical content queued another job
// and peers burned sandbox time re-proving the same artifact.
func (a *api) queueCrossVerification(ctx context.Context, sampleID string) error {
	if row, ok, err := a.d.Store.GetSample(ctx, sampleID); err == nil && ok {
		switch strings.ToUpper(row.Status) {
		case "CROSS_PASS", "MATRIX_PASS", "STABLE":
			return nil // already reproduced by another peer
		}
	}
	jobs, err := a.d.Store.JobsForSample(ctx, sampleID)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			return nil // work is already queued or in flight
		}
	}
	_, err = a.d.Store.CreateJob(ctx, serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open",
	})
	return err
}

// checkArtifactStatic enforces the C13 static artifact rules on the raw
// tar.gz bytes.
func checkArtifactStatic(data []byte, manifest domain.SampleManifest) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return errors.New("artifact is not valid gzip")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	entries := 0
	var manifestInArtifact []byte
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("artifact is not a valid tar archive")
		}
		entries++
		if entries > maxArtifactFiles {
			return errors.New("artifact has more than 200 entries")
		}
		name := hdr.Name
		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return errors.New("artifact entry " + name + " is a symlink or hardlink")
		case tar.TypeDir, tar.TypeReg:
		default:
			return errors.New("artifact entry " + name + " has an unsupported type")
		}
		if err := checkEntryName(name); err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(tr, maxArtifactBytes+1))
		if err != nil {
			return errors.New("reading artifact entry " + name + " failed")
		}
		if bytes.IndexByte(content, 0) >= 0 {
			return errors.New("artifact entry " + name + " looks binary (NUL byte)")
		}
		if strings.TrimPrefix(name, "./") == "csx.json" {
			manifestInArtifact = content
		}
	}
	if manifestInArtifact == nil {
		return errors.New("artifact is missing csx.json")
	}

	// csx.json must equal the posted manifest (canonical-JSON comparison).
	var inArtifact domain.SampleManifest
	if err := json.Unmarshal(manifestInArtifact, &inArtifact); err != nil {
		return errors.New("csx.json inside artifact is not valid JSON")
	}
	normalized := manifest
	if normalized.License == "" {
		normalized.License = "MIT-0"
	}
	if inArtifact.License == "" {
		inArtifact.License = "MIT-0"
	}
	if !bytes.Equal(domain.MustCanonicalJSON(inArtifact), domain.MustCanonicalJSON(normalized)) {
		return errors.New("csx.json inside artifact does not match the posted manifest")
	}
	return nil
}

// checkEntryName rejects absolute, traversal, and forbidden-directory names.
func checkEntryName(name string) error {
	if name == "" {
		return errors.New("artifact entry with empty name")
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") ||
		(len(name) >= 2 && name[1] == ':') {
		return errors.New("artifact entry " + name + " has an absolute path")
	}
	if strings.Contains(name, "\\") {
		return errors.New("artifact entry " + name + " uses backslash separators")
	}
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if seg == ".." {
			return errors.New("artifact entry " + name + " contains a path traversal")
		}
		if forbiddenDirs[seg] {
			return errors.New("artifact entry " + name + " is inside forbidden directory " + seg)
		}
		if seg == ".env" {
			return errors.New("artifact entry " + name + " is a .env file")
		}
	}
	return nil
}

// handleSampleMeta implements GET /v1/samples/{sampleId}.
func (a *api) handleSampleMeta(w http.ResponseWriter, r *http.Request) {
	sampleID := r.PathValue("sampleId")
	row, ok, err := a.d.Store.GetSample(r.Context(), sampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sample lookup failed")
		return
	}
	// GetSample deliberately does not filter, because the operator commands
	// and the audit trail need the row. Every SERVING read has to check the
	// flag itself, and this one did not: a quarantined sample kept answering
	// here with its old status and no hint it had been withdrawn, so anyone
	// holding the content address went on being told it was verified.
	if !ok || row.Quarantined {
		writeErr(w, http.StatusNotFound, "sample not found")
		return
	}
	receipts, err := a.d.Store.ReceiptsForSample(r.Context(), sampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "receipt lookup failed")
		return
	}
	type receiptSummary struct {
		ReceiptID      string `json:"receiptId"`
		PeerID         string `json:"peerId"`
		EnvHash        string `json:"envHash"`
		ContractResult string `json:"contractResult,omitempty"`
		CreatedAt      string `json:"createdAt,omitempty"`
	}
	summaries := []receiptSummary{}
	for _, rr := range receipts {
		s := receiptSummary{
			ReceiptID: rr.ReceiptID, PeerID: rr.PeerID, EnvHash: rr.EnvHash,
			ContractResult: rr.ContractResult,
		}
		if !rr.CreatedAt.IsZero() {
			s.CreatedAt = rr.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
		}
		summaries = append(summaries, s)
	}
	resp := map[string]any{
		"sampleId":     row.SampleID,
		"status":       row.Status,
		"license":      row.License,
		"originSeeder": row.OriginSeeder,
		"sizeBytes":    row.SizeBytes,
		"manifest":     json.RawMessage(row.ManifestJSON),
		"receipts":     summaries,
	}
	if !row.CreatedAt.IsZero() {
		resp["createdAt"] = row.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSampleArtifact implements GET /v1/samples/{sampleId}/artifact — the
// Main Seeder fallback (§15.1): the server always holds one copy.
func (a *api) handleSampleArtifact(w http.ResponseWriter, r *http.Request) {
	if a.d.Blobs == nil {
		writeErr(w, http.StatusServiceUnavailable, "sample storage not configured")
		return
	}
	sampleID := r.PathValue("sampleId")
	row, ok, err := a.d.Store.GetSample(r.Context(), sampleID)
	if err != nil || !ok || row.Quarantined {
		writeErr(w, http.StatusNotFound, "sample not found")
		return
	}
	rc, err := a.d.Blobs.Get(r.Context(), sampleID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "artifact not available")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("ETag", `"`+strings.TrimPrefix(sampleID, "sha256:")+`"`)
	_, _ = io.Copy(w, rc)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
