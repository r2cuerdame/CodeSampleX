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
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"slices"
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
	computedCaseID := cse.ComputeID()
	if cse.CaseID != "" && cse.CaseID != computedCaseID {
		writeErr(w, http.StatusBadRequest, "caseId does not match case content")
		return
	}
	cse.CaseID = computedCaseID
	// Keep stored metadata coherent for legacy clients that omitted the
	// derived field. The artifact remains byte-identical and content-addressed;
	// only the server's parsed manifest gains the ID it just verified.
	manifest.Case = cse
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
// maxCrossAttempts bounds how many times one sample may be offered for
// cross verification.
//
// A sample nothing can resolve would otherwise cycle through every verifier
// forever, and the queue it crowds is the one real work waits in. Four is
// enough to outlast a single misconfigured machine — production ran four
// verifiers, two of which failed every resolve they touched — without
// letting a genuinely unbuildable sample become permanent work.
const maxCrossAttempts = 4

// requeueCrossVerification offers a sample for cross verification again
// after an attempt ended without the contract running.
//
// SKIPPED is not FAIL. A FAIL is a measurement OF the sample: the contract
// ran and did not hold, and asking another machine to run it again is asking
// the network to keep trying until it hears what it wants. A SKIPPED contract
// is the verifier reporting that it measured nothing — dependency resolution
// died before the sample was ever exercised — and closing the work on that
// throws away the sample rather than the verifier.
//
// Production stranded 159 authoring drafts on exactly this: two of its four
// verifiers failed resolve on every job they claimed, each failure marked the
// sample's only cross job done, and nothing ever queued another.
func (a *api) requeueCrossVerification(ctx context.Context, sampleID, contractResult string) {
	if !strings.EqualFold(strings.TrimSpace(contractResult), "SKIPPED") {
		return
	}
	jobs, err := a.d.Store.JobsForSample(ctx, sampleID)
	if err != nil {
		return
	}
	attempts := 0
	for _, j := range jobs {
		if j.Reason == "cross" {
			attempts++
		}
	}
	if attempts >= maxCrossAttempts {
		return
	}
	// Best effort. A draft that misses its retry is no worse off than it was
	// before this existed, and a failed enqueue must not fail the receipt the
	// verifier just signed.
	_ = a.queueCrossVerification(ctx, sampleID)
}

func (a *api) queueCrossVerification(ctx context.Context, sampleID string) error {
	return queueCrossVerificationOn(ctx, a.d.Store, sampleID)
}

// ReconcileStrandedDrafts offers every stranded authoring draft for cross
// verification again, and reports how many it woke.
//
// The retry on the receipt path only fires when an attempt CLOSES a job, so
// the drafts stranded before that code existed have no future event to reach
// them: their job is done, no receipt passed, and nothing will ever look at
// them again. This runs once at boot, which is often enough — deploys are
// frequent and the backlog is finite — and stays off every request path.
//
// It is idempotent by construction: queueCrossVerificationOn declines a
// sample that already has work queued, and StrandedDrafts excludes anything
// that has spent its attempts.
func ReconcileStrandedDrafts(ctx context.Context, store serverstore.Store, limit int) (int, error) {
	ids, err := store.StrandedDrafts(ctx, maxCrossAttempts, limit)
	if err != nil {
		return 0, err
	}
	woken := 0
	for _, id := range ids {
		if err := queueCrossVerificationOn(ctx, store, id); err != nil {
			// One unqueueable draft must not stop the rest: the next boot
			// tries again, and the others are waiting now.
			continue
		}
		woken++
	}
	return woken, nil
}

func queueCrossVerificationOn(ctx context.Context, store serverstore.Store, sampleID string) error {
	var manifest domain.SampleManifest
	if row, ok, err := store.GetSample(ctx, sampleID); err == nil && ok {
		switch strings.ToUpper(row.Status) {
		case "CROSS_PASS", "MATRIX_PASS", "STABLE":
			return nil // already reproduced by another peer
		}
		if err := json.Unmarshal([]byte(row.ManifestJSON), &manifest); err != nil {
			return fmt.Errorf("decode sample manifest for worker requirements: %w", err)
		}
	}
	jobs, err := store.JobsForSample(ctx, sampleID)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.Reason == "cross" && (j.Status == "open" || j.Status == "claimed") {
			return nil // work is already queued or in flight
		}
	}
	// A cross job asks a DIFFERENT machine to reproduce the result, so its
	// requirements must describe the runtime LINE, not the author's exact
	// patch level. Copying manifest.Environment.RuntimeVersion verbatim
	// asked every verifier for node 22.23.2 or go 1.26.5 exactly; no pinned
	// image can promise a patch digit, so the receipt was refused on
	// arrival, and because that refusal is the same statement that would
	// have closed the job, the job stayed claimed forever. Jobs pinned to a
	// major completed normally throughout — that is the precision a cross
	// job can actually ask for. Matrix jobs, which exist precisely to pin an
	// exact version, are built elsewhere and keep their exactness.
	requirements := domain.WorkerRequirements{
		SandboxCapability: domain.CapContainerRun,
		VerifierAdapter:   manifest.VerifierAdapter,
		// The OS the sample ran on is the OS a reproduction must run on. The
		// queue filters offers on it, so a Linux verifier is no longer handed
		// Windows-only work to fail resolve on and burn the sample's bounded
		// cross attempts.
		OS:        crossJobOS(manifest.Environment),
		Ecosystem: manifest.Environment.Ecosystem,
		Runtime:   manifest.Environment.Runtime,
		RuntimeVersion: domain.RuntimeLine(manifest.Environment.Runtime,
			manifest.Environment.RuntimeVersion),
		ExecutionContext: manifest.Environment.ExecutionContext,
		BrowserFamily:    manifest.Environment.BrowserFamily,
		// Browser and engine versions are already majors by construction.
		BrowserMajor:  manifest.Environment.BrowserMajor,
		Engine:        manifest.Environment.Engine,
		EngineVersion: manifest.Environment.EngineVersion,
	}
	// Only installed engines/SDKs are host requirements. Ordinary framework
	// libraries are resolved inside the disposable container and must not
	// unnecessarily exclude otherwise capable workers.
	for _, framework := range manifest.Environment.Frameworks {
		if _, ok := domain.WantedTargetFromFramework(framework); ok {
			requirements.Frameworks = append(requirements.Frameworks, framework)
		}
	}
	_, err = store.CreateJob(ctx, serverstore.JobRow{
		SampleID: sampleID, Reason: "cross", Status: "open",
		WantEnvJSON: string(domain.MustCanonicalJSON(requirements)),
	})
	return err
}

// crossJobOS pins the platform a reproduction must run on — and only when the
// manifest's environment IS an execution environment. A farm-authored draft
// records the container it ran in (virtualization "container"), and that OS
// is the platform the sample answers for. A user proposal records the
// author's HOST — a Windows laptop proposing an npm sample — and pinning
// that OS would strand the sample: no npm verifier serves Windows. Those
// stay unpinned, which is every cross job's behaviour before the field
// existed.
func crossJobOS(env domain.EnvironmentFingerprint) string {
	if !strings.EqualFold(strings.TrimSpace(env.Virtualization), "container") {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(env.OS))
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
	lockfiles := map[string]string{}
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
		// The entry was already drained into content above. Reading the
		// tar reader a SECOND time returns zero bytes and a nil error, so
		// every lockfile was stored as "" and checkDeclaredVersions --
		// which exists to refuse a manifest naming a version its own
		// lockfile never resolved -- could not fire on any upload ever
		// made. It saw a non-empty map, so it did not early-return either;
		// it just never found a version. Only its own unit tests, which
		// build the map by hand, ever exercised it.
		if base := path.Base(strings.TrimPrefix(name, "./")); lockfileForVersionCheck[strings.ToLower(base)] {
			lockfiles[strings.ToLower(base)] = string(content)
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
	return checkDeclaredVersions(manifest, lockfiles)
}

// lockfileForVersionCheck are the lockfiles whose format is unambiguous
// enough to say "this package resolved to that version" by reading it.
var lockfileForVersionCheck = map[string]bool{
	"cargo.lock": true, "package-lock.json": true, "gemfile.lock": true,
}

// checkDeclaredVersions refuses a manifest that names a version its own
// lockfile did not resolve.
//
// Two published samples said so. A Cargo.toml written as version = "0.4.42"
// is a CARET requirement, cargo resolved 0.4.45, the contract compiled and
// passed against 0.4.45 — and the manifest went on claiming 0.4.42. The
// sample was therefore evidence about a version it had never been run on,
// which is the one thing this whole network exists not to do, and nothing
// anywhere would have caught it.
//
// Silence is deliberate where the format is not listed or the package is
// absent: a false refusal here blocks an honest sample, and the check is
// only worth having while it is certain.
func checkDeclaredVersions(manifest domain.SampleManifest, lockfiles map[string]string) error {
	if len(lockfiles) == 0 {
		return nil
	}
	for _, raw := range manifest.Packages {
		p, err := domain.ParsePURL(raw)
		if err != nil || p.Version == "" {
			continue
		}
		for name, body := range lockfiles {
			resolved := resolvedVersionsIn(name, body, p)
			if len(resolved) == 0 {
				continue
			}
			// A lockfile routinely pins SEVERAL versions of one package:
			// a Rust workspace holds syn 1 for a transitive dependency and
			// syn 2 for the direct one, and reading only the first match
			// refused a manifest that named the version it actually used.
			// The manifest is wrong only when the version it declares
			// appears nowhere in the lockfile at all.
			if slices.Contains(resolved, p.Version) {
				continue
			}
			return errors.New("manifest declares " + raw + " but " + name +
				" resolved " + p.Name + " " + strings.Join(resolved, ", ") +
				" — the contract ran against the lockfile, so the manifest is wrong")
		}
	}
	return nil
}

// resolvedVersionsIn reads EVERY version a lockfile pins for one package.
//
// It used to return the first match and a found flag, which is a different
// question: a lockfile can pin several versions of the same package at
// once, and the first one in the file is not necessarily the one the
// contract used. Returning all of them lets the caller ask what it
// actually wants — is the declared version in here — instead of comparing
// against whichever happened to be listed first.
//
// An empty result means "cannot tell", and the caller treats that as no
// objection.
func resolvedVersionsIn(lockfile, body string, p domain.PURL) []string {
	var out []string
	switch lockfile {
	case "cargo.lock":
		re := regexp.MustCompile(`(?m)^name = "` + regexp.QuoteMeta(p.Name) + `"\n?^version = "([^"]+)"`)
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	case "gemfile.lock":
		re := regexp.MustCompile(`(?m)^\s{4}` + regexp.QuoteMeta(p.Name) + ` \(([^)]+)\)`)
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
	case "package-lock.json":
		var lock struct {
			Packages map[string]struct {
				Version string `json:"version"`
			} `json:"packages"`
		}
		if json.Unmarshal([]byte(body), &lock) != nil {
			return nil
		}
		// npm nests duplicates under a dependent's own node_modules, and
		// every one of them is a version this tree really installs.
		suffix := "node_modules/" + p.Name
		for key, e := range lock.Packages {
			if e.Version == "" {
				continue
			}
			if key == suffix || strings.HasSuffix(key, "/"+suffix) {
				out = append(out, e.Version)
			}
		}
	}
	return out
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
	if err != nil || !ok {
		writeErr(w, http.StatusNotFound, "sample not found")
		return
	}
	if row.Quarantined {
		jobID, parseErr := strconv.ParseInt(strings.TrimSpace(r.Header.Get(domain.VerificationJobIDHeader)), 10, 64)
		peerID := strings.TrimSpace(r.Header.Get(domain.VerificationPeerIDHeader))
		job, found, jobErr := a.d.Store.Job(r.Context(), jobID)
		if row.Status != "DRAFT" || parseErr != nil || jobID <= 0 || !validPeerID(peerID) || jobErr != nil || !found ||
			job.SampleID != sampleID || job.Reason != "cross" || job.Status != "claimed" || job.ClaimedBy != peerID {
			writeErr(w, http.StatusNotFound, "sample not found")
			return
		}
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
