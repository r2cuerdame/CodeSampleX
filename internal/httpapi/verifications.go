package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// stableWindow: STABLE requires no FAIL receipt within this window (C13).
const stableWindow = 30 * 24 * time.Hour

// receiptDescribesWhereItRan rejects a receipt whose environment cannot be
// the environment its own capability describes.
//
// This is not hypothetical. A peer running a build from before the sandbox
// started rewriting the stage environment signed 271 receipts that said
// CONTAINER_RUN and then described the Windows host that launched docker.
// Every one was a valid signature over a false statement, and the graph
// took them: 39 contract failures produced by a container were filed as
// Windows results, and 39 sample pages showed a user a FAIL on an
// environment where it had never been tried.
//
// A signature proves who wrote a receipt, never that the receipt is true.
// The checks below are the ones that can be made from the receipt alone —
// each rejects a statement the receipt itself contradicts, so no honest
// peer can trip them, and neither an old build nor a lying one can put a
// result under an environment it did not run in.
func receiptDescribesWhereItRan(receipt domain.VerificationReceipt) error {
	env := receipt.Environment
	if receipt.SandboxCapability == domain.CapContainerRun {
		// A container run is described by the container. A receipt naming
		// the host that started docker is describing the wrong machine.
		if env.Virtualization != "container" {
			return errors.New("receipt claims CONTAINER_RUN but its environment is not a container")
		}
		// A Docker daemon serves Linux or Windows containers. Both are
		// real containers and both are honest evidence; what the check
		// above still refuses is the case that caused the incident in the
		// first place — a HOST describing itself as the container it
		// started. Anything that is neither is not a container OS we run.
		if env.OS != "linux" && env.OS != "windows" {
			return errors.New("receipt claims CONTAINER_RUN but its environment is neither linux nor windows")
		}
	}
	// The graph groups by environmentHash, so a hash that does not belong to
	// the attached environment files the result under someone else's.
	if receipt.EnvironmentHash != "" && receipt.EnvironmentHash != env.Hash() {
		return errors.New("receipt environmentHash does not match its environment")
	}
	return nil
}

// receiptResolvedPackagesMatchSample validates the claims that can be checked
// without rerunning the resolver. An empty list is deliberately valid: it
// means the peer could not establish a resolved version. A non-empty list is
// evidence only after a successful resolve, and it may name a different
// version of a package the sample declared, but never a different package.
func receiptResolvedPackagesMatchSample(receipt domain.VerificationReceipt, manifest domain.SampleManifest) error {
	if receipt.ResolvedPackages == nil {
		return nil
	}
	if len(receipt.ResolvedPackages) == 0 {
		return errors.New("resolvedPackages must be omitted rather than empty")
	}
	if receipt.Stages["resolve"] != string(domain.ResultPass) {
		return errors.New("resolvedPackages requires a PASS resolve stage")
	}

	receiptEcosystem := strings.ToLower(strings.TrimSpace(receipt.Environment.Ecosystem))
	manifestEcosystem := strings.ToLower(strings.TrimSpace(manifest.Environment.Ecosystem))
	if receiptEcosystem == "" || manifestEcosystem == "" || receiptEcosystem != manifestEcosystem {
		return errors.New("resolvedPackages requires matching receipt and sample ecosystems")
	}

	parsed := make([]domain.PURL, len(receipt.ResolvedPackages))
	for i, raw := range receipt.ResolvedPackages {
		p, err := domain.ParsePURL(raw)
		if err != nil {
			return errors.New("resolvedPackages contains an invalid purl")
		}
		if p.String() != raw {
			return errors.New("resolvedPackages must contain canonical purls")
		}
		if !domain.ConcreteResolvedVersion(p.Version) {
			return errors.New("resolvedPackages must contain concrete resolved versions")
		}
		if p.Ecosystem != receiptEcosystem {
			return errors.New("resolvedPackages contains a package from a resolver that did not run")
		}
		if i > 0 {
			switch {
			case raw == receipt.ResolvedPackages[i-1]:
				return errors.New("resolvedPackages must not contain duplicates")
			case raw < receipt.ResolvedPackages[i-1]:
				return errors.New("resolvedPackages must be sorted")
			}
		}
		parsed[i] = p
	}

	type packageKey struct {
		ecosystem string
		name      string
	}
	declared := make(map[packageKey]bool, len(manifest.Packages))
	for _, raw := range manifest.Packages {
		if p, err := domain.ParsePURL(raw); err == nil {
			declared[packageKey{ecosystem: p.Ecosystem, name: p.Name}] = true
		}
	}
	for _, p := range parsed {
		if !declared[packageKey{ecosystem: p.Ecosystem, name: p.Name}] {
			return errors.New("resolvedPackages contains a package not declared by the sample")
		}
	}
	return nil
}

// readVerificationReceipt enforces the receipt schemas' closed wire shape.
// A signature only covers fields represented by VerificationReceipt; if an
// unknown field or a second JSON document were silently discarded, the
// server would accept and store a different statement from the bytes the
// sender presented.
func readVerificationReceipt(w http.ResponseWriter, r *http.Request, receipt *domain.VerificationReceipt) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(receipt); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeErr(w, http.StatusBadRequest, "invalid verification receipt: "+err.Error())
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeErr(w, http.StatusBadRequest, "verification receipt must contain exactly one JSON document")
		return false
	}
	return true
}

func decodeWorkerRequirements(raw string) (domain.WorkerRequirements, error) {
	var want domain.WorkerRequirements
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		return want, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return want, errors.New("wantEnv must contain exactly one JSON object")
	}
	return want, nil
}

// versionSatisfies reports whether a measured version answers a wanted
// one, comparing whole dot-separated components.
//
// A requirement states the precision it cares about: "21" is answered by
// "21.0.5", and "21.0.5" is answered only by itself. String equality
// refused the first case, so a job asking for a runtime LINE could never
// be satisfied by a real machine — the container reports its patch level
// and nothing else. Components are compared whole so "2" never matches
// "21", which a plain string prefix would have allowed.
func versionSatisfies(want, got string) bool {
	want, got = strings.TrimSpace(want), strings.TrimSpace(got)
	if want == "" {
		return true
	}
	if want == got {
		return true
	}
	wantParts, gotParts := strings.Split(want, "."), strings.Split(got, ".")
	if len(wantParts) > len(gotParts) {
		return false
	}
	for i, part := range wantParts {
		if part != gotParts[i] {
			return false
		}
	}
	return true
}

func receiptMatchesRequirements(receipt domain.VerificationReceipt, want domain.WorkerRequirements) bool {
	env := receipt.Environment.Normalize()
	if want.SandboxCapability != "" && receipt.SandboxCapability != want.SandboxCapability {
		return false
	}
	if want.VerifierAdapter != "" && receipt.VerifierAdapter != want.VerifierAdapter {
		return false
	}
	// A job that pins an OS is answered only by a receipt that ran there; a
	// claim that slipped past the queue filter must not stamp the wrong
	// platform.
	if want.OS != "" && !strings.EqualFold(want.OS, env.OS) {
		return false
	}
	checks := [][2]string{
		{want.Ecosystem, env.Ecosystem}, {want.Runtime, env.Runtime},
		{want.ExecutionContext, env.ExecutionContext},
		{want.BrowserFamily, env.BrowserFamily},
		{want.Engine, env.Engine},
	}
	for _, pair := range checks {
		if pair[0] != "" && pair[0] != pair[1] {
			return false
		}
	}
	versions := [][2]string{
		{want.RuntimeVersion, env.RuntimeVersion},
		{want.BrowserMajor, env.BrowserMajor},
		{want.EngineVersion, env.EngineVersion},
	}
	for _, pair := range versions {
		if !versionSatisfies(pair[0], pair[1]) {
			return false
		}
	}
	for _, required := range want.Frameworks {
		found := false
		for _, got := range env.Frameworks {
			if strings.EqualFold(strings.TrimSpace(required), strings.TrimSpace(got)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func exactSupportedMatrixRequirements(want domain.WorkerRequirements) bool {
	javaLine := map[string]bool{"8": true, "11": true, "17": true, "21": true, "25": true}
	return want.SandboxCapability == domain.CapContainerRun &&
		(want.VerifierAdapter == "maven-java@1" || want.VerifierAdapter == "gradle-java@1") &&
		want.Ecosystem == "maven" && want.Runtime == "java" && javaLine[want.RuntimeVersion] &&
		want.ExecutionContext == "java" && want.BrowserFamily == "" && want.BrowserMajor == "" &&
		want.Engine == "" && want.EngineVersion == "" && len(want.Frameworks) == 0
}

func matrixRequirementsMatchManifest(receipt domain.VerificationReceipt, want domain.WorkerRequirements, manifest domain.SampleManifest) bool {
	env := manifest.Environment.Normalize()
	if manifest.VerifierAdapter != want.VerifierAdapter || env.Ecosystem != want.Ecosystem || env.Runtime != want.Runtime ||
		(env.ExecutionContext != "" && env.ExecutionContext != want.ExecutionContext) ||
		(env.Language != "" && env.Language != "java") {
		return false
	}
	lines := map[string]int{"8": 8, "11": 11, "17": 17, "21": 21, "25": 25}
	target := env.LanguageVersion
	if target == "" {
		target = want.RuntimeVersion
	}
	return lines[target] != 0 && lines[target] <= lines[want.RuntimeVersion] &&
		receipt.Environment.Normalize().LanguageVersion == target
}

// handleVerification implements POST /v1/verifications: verify the ed25519
// signature and the peerId↔pubkey binding, persist the immutable receipt,
// then recompute the sample's status per the BINDING transition rules:
//
//	PUBLISHED   → CROSS_PASS   first contract-PASS receipt from a peer ≠ origin
//	            → MATRIX_PASS  receipts span ≥2 distinct context boundaries
//	                           (os OR runtime major OR browserFamily)
//	            → STABLE       ≥3 distinct passing peers ∧ no FAIL in 30d
func (a *api) handleVerification(w http.ResponseWriter, r *http.Request) {
	var receipt domain.VerificationReceipt
	if !readVerificationReceipt(w, r, &receipt) {
		return
	}
	switch receipt.SchemaVersion {
	case 1:
		// resolvedPackages did not exist in the public v1 schema. Keeping the
		// version boundary strict prevents a document from claiming v1 while
		// relying on v2 semantics.
		// A present empty array is still not a v1 document. json.Unmarshal
		// preserves absent (nil) versus [] (non-nil), so enforce the schema
		// boundary instead of only rejecting arrays that happen to carry a
		// claim.
		if receipt.ResolvedPackages != nil {
			writeErr(w, http.StatusBadRequest, "receipt schemaVersion 1 must not contain resolvedPackages")
			return
		}
	case 2:
		// v2 adds resolvedPackages. Its claims are checked against the sample
		// after the signature and sample identity have been established.
		// Check the present-empty wire form before signature verification:
		// omitempty necessarily canonicalizes [] as absent, so accepting it
		// would make a schema-valid-looking document impossible to sign in
		// the same form this server verifies.
		if receipt.ResolvedPackages != nil && len(receipt.ResolvedPackages) == 0 {
			writeErr(w, http.StatusBadRequest, "resolvedPackages must be omitted rather than empty")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "receipt schemaVersion must be 1 or 2")
		return
	}
	if receipt.SampleID == "" || receipt.PeerID == "" || receipt.PeerPubkey == "" || receipt.PeerSignature == "" {
		writeErr(w, http.StatusBadRequest, "receipt requires sampleId, peerId, peerPubkey and peerSignature")
		return
	}

	// Signature over the canonical receipt (signature field cleared).
	if !identity.Verify(receipt.PeerPubkey, receipt.PeerSignature, receipt.SigningBytes()) {
		writeErr(w, http.StatusBadRequest, "invalid receipt signature")
		return
	}
	// peerId must be the fingerprint of the embedded pubkey — a valid
	// signature under someone else's name is still a forgery.
	if derivePeerID(receipt.PeerPubkey) != receipt.PeerID {
		writeErr(w, http.StatusBadRequest, "peerId does not match peerPubkey fingerprint")
		return
	}

	if err := receiptDescribesWhereItRan(receipt); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	sample, ok, err := a.d.Store.GetSample(ctx, receipt.SampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sample lookup failed")
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "unknown sample")
		return
	}
	var manifest domain.SampleManifest
	if err := json.Unmarshal([]byte(sample.ManifestJSON), &manifest); err != nil {
		writeErr(w, http.StatusInternalServerError, "stored sample manifest is invalid")
		return
	}
	if receipt.SchemaVersion == 2 {
		if err := receiptResolvedPackagesMatchSample(receipt, manifest); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	var claimedJobID int64
	if rawJobID := strings.TrimSpace(r.Header.Get(domain.VerificationJobIDHeader)); rawJobID != "" {
		claimedJobID, err = strconv.ParseInt(rawJobID, 10, 64)
		if err != nil || claimedJobID <= 0 {
			writeErr(w, http.StatusBadRequest, "invalid verification job id")
			return
		}
		job, found, lookupErr := a.d.Store.Job(ctx, claimedJobID)
		if lookupErr != nil {
			writeErr(w, http.StatusInternalServerError, "verification job lookup failed")
			return
		}
		if !found {
			writeErr(w, http.StatusNotFound, "verification job not found")
			return
		}
		if job.Status != "claimed" || job.ClaimedBy != receipt.PeerID || job.SampleID != receipt.SampleID ||
			(job.Reason != "cross" && job.Reason != "matrix") {
			writeErr(w, http.StatusConflict, "receipt does not match the claimed verification job")
			return
		}
		if job.WantEnvJSON != "" {
			want, parseErr := decodeWorkerRequirements(job.WantEnvJSON)
			if parseErr != nil || (job.Reason == "matrix" &&
				(!exactSupportedMatrixRequirements(want) || !matrixRequirementsMatchManifest(receipt, want, manifest))) ||
				!receiptMatchesRequirements(receipt, want) {
				writeErr(w, http.StatusConflict, "receipt environment does not match the claimed verification job")
				return
			}
		} else if job.Reason == "matrix" {
			writeErr(w, http.StatusConflict, "matrix verification job has no exact environment")
			return
		}
	}

	contractResult := receipt.Stages["contract"]
	receiptRow := serverstore.ReceiptRow{
		ReceiptID:      receipt.ReceiptID(),
		SampleID:       receipt.SampleID,
		PeerID:         receipt.PeerID,
		EnvHash:        receipt.EnvironmentHash,
		ReceiptJSON:    string(domain.MustCanonicalJSON(receipt)),
		ContractResult: contractResult,
	}
	if claimedJobID != 0 {
		accepted, saveErr := a.d.Store.SaveReceiptForJob(ctx, receiptRow, claimedJobID)
		if saveErr != nil {
			writeErr(w, http.StatusInternalServerError, "saving job receipt failed")
			return
		}
		if !accepted {
			writeErr(w, http.StatusConflict, "verification job claim expired or changed")
			return
		}
		// SaveReceiptForJob atomically promotes a quarantined authoring draft
		// on an independently signed contract PASS. Reload so the response and
		// the monotonic status calculation observe that committed state.
		refreshed, found, reloadErr := a.d.Store.GetSample(ctx, receipt.SampleID)
		if reloadErr != nil || !found {
			writeErr(w, http.StatusInternalServerError, "reloading verified sample failed")
			return
		}
		sample = refreshed
	} else {
		if err := a.d.Store.SaveReceipt(ctx, receiptRow); err != nil {
			writeErr(w, http.StatusInternalServerError, "saving receipt failed")
			return
		}
		// An unbound receipt may answer generic independent-cross work only.
		// Targeted matrix jobs require the exact atomic path above.
		_ = a.d.Store.CompleteJobsForSample(ctx, receipt.SampleID, receipt.PeerID)
	}

	receipts, err := a.d.Store.ReceiptsForSample(ctx, receipt.SampleID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "receipt lookup failed")
		return
	}
	newStatus := sampleStatusFromReceipts(sample.Status, receipts, a.now())
	if newStatus != sample.Status {
		if err := a.d.Store.SetSampleStatus(ctx, receipt.SampleID, newStatus); err != nil {
			writeErr(w, http.StatusInternalServerError, "updating sample status failed")
			return
		}
	}
	// The attempt closed the job whatever it reported. If the contract never
	// ran, the sample has not been measured and still needs a machine that
	// can measure it.
	if claimedJobID != 0 && statusRank(newStatus) < statusRank("CROSS_PASS") {
		a.requeueCrossVerification(ctx, receipt.SampleID, contractResult)
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":       "accepted",
		"sampleStatus": newStatus,
	})
}

// derivePeerID computes "ed25519:" + hex(sha256(pubkey))[:16].
func derivePeerID(pubB64 string) string {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:])[:16]
}

// sampleStatusFromReceipts applies the C13 transition rules. Status only
// ever upgrades, and CROSS_PASS requires a pass from a peer other than the
// one that originated the sample.
func sampleStatusFromReceipts(current string, rows []serverstore.ReceiptRow, now time.Time) string {
	if len(rows) == 0 {
		return current
	}
	// The origin is the first peer to ATTEST anything, not the first to
	// write a row.
	//
	// Taking rows[0] meant any stranger could become the origin by filing a
	// FAILING receipt first — and the author's own passing receipt then
	// counted as the independent confirmation, driving the sample to
	// CROSS_PASS with exactly one real party behind it. Independence is the
	// single thing a cross pass asserts that a publisher cannot manufacture
	// alone, and this handed it over for the price of a failed run.
	//
	// With no pass at all, no cross can be claimed either way, so the old
	// reading is harmless there and is kept as the fallback.
	origin := rows[0].PeerID
	for _, row := range rows {
		if row.ContractResult == string(domain.ResultPass) {
			origin = row.PeerID
			break
		}
	}

	crossPass := false
	passPeers := map[string]bool{}
	recentFail := false
	var infos []compatibility.ReceiptInfo
	for _, row := range rows {
		switch row.ContractResult {
		case string(domain.ResultPass):
			passPeers[row.PeerID] = true
			if row.PeerID != origin {
				crossPass = true
			}
			if info, ok := compatibility.ParseReceiptRow(row); ok {
				infos = append(infos, info)
			}
		case string(domain.ResultFail):
			if row.CreatedAt.IsZero() || now.Sub(row.CreatedAt) <= stableWindow {
				recentFail = true
			}
		}
	}

	status := current
	rank := statusRank(status)
	if crossPass && rank < statusRank("CROSS_PASS") {
		status, rank = "CROSS_PASS", statusRank("CROSS_PASS")
	}
	if crossPass && spansContextBoundary(infos) && rank < statusRank("MATRIX_PASS") {
		status, rank = "MATRIX_PASS", statusRank("MATRIX_PASS")
	}
	if len(passPeers) >= 3 && !recentFail && rank < statusRank("STABLE") {
		status = "STABLE"
	}
	return status
}

func statusRank(status string) int {
	switch status {
	case "STABLE":
		return 4
	case "MATRIX_PASS":
		return 3
	case "CROSS_PASS":
		return 2
	case "PUBLISHED":
		return 1
	}
	return 0
}

// spansContextBoundary reports whether passing receipts cover ≥2 distinct
// values of at least one context boundary: os, runtime major, or browser
// family (docs/execution-context.md §4).
// describesAnEnvironment reports whether a receipt says enough about where
// it ran to count as a distinct environment.
//
// An OS name with no runtime and no version is not a different environment,
// it is an unknown one, and the two must never be conflated. Receipts
// written before the runner stamped the sandbox environment claim the HOST
// os with every other field blank — 103 of them on the live network, all
// saying "windows" for contracts that actually executed in a linux
// container. Counting those as diversity is what granted every MATRIX_PASS
// on the network, and it is also exactly the hole a Sybil would drive
// through: minting environments is free if they may be blank.
func describesAnEnvironment(env domain.EnvironmentFingerprint) bool {
	return env.OS != "" && env.Runtime != "" && env.RuntimeVersion != ""
}

// spansContextBoundary implements the L5 rule — reproduced across a real
// OS/runtime/version boundary (goal.md §6.2) — counting only receipts that
// actually describe where they ran.
func spansContextBoundary(infos []compatibility.ReceiptInfo) bool {
	oses := map[string]bool{}
	runtimeMajors := map[string]bool{}
	browsers := map[string]bool{}
	for _, info := range infos {
		env := info.Env
		if !describesAnEnvironment(env) {
			continue // unknown, not different
		}
		oses[env.OS] = true
		// The release LINE, not the major segment. Go, Python, Elixir and
		// Dart keep their whole history in the second segment, so go1.9 and
		// go1.26 both bucketed to "go@1" — seven years apart and counted as
		// the same environment. That withholds L5 from a sample two peers
		// genuinely reproduced across a real boundary, which is the harmless
		// direction, but it is still the wrong answer and the same rule the
		// grader and the client both use.
		runtimeMajors[env.Runtime+"@"+runtimeLine(env.Runtime, env.RuntimeVersion)] = true
		if env.BrowserFamily != "" {
			browsers[env.BrowserFamily] = true
		}
	}
	return len(oses) >= 2 || len(runtimeMajors) >= 2 || len(browsers) >= 2
}

// handleJobsList implements GET /v1/verification/jobs?peerId=&capability=&reason=&limit=.
func (a *api) handleJobsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	capability := q.Get("capability")
	reason := q.Get("reason")
	if reason != "" && reason != "cross" && reason != "matrix" {
		writeErr(w, http.StatusBadRequest, "reason must be cross or matrix")
		return
	}
	limit := 10
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	// peerId has always been sent by the client and never read. A peer
	// that already filed a receipt for a sample cannot cross-verify it, and
	// offering it the job takes that job away from a peer who could.
	type jobOut struct {
		ID       int64           `json:"id"`
		SampleID string          `json:"sampleId"`
		Reason   string          `json:"reason"`
		WantEnv  json.RawMessage `json:"wantEnv,omitempty"`
	}
	out := []jobOut{}
	// Page through the ordered queue until enough live artifacts are found.
	// Missing blobs remain untouched for audit/recovery and can never form a
	// permanent head-of-line barrier, even if there are more than one page.
	const pageSize = 100
	for offset := 0; len(out) < limit; {
		// containerOs is what the verifier's Docker daemon actually serves.
		// Absent, the queue is unfiltered by platform, which is how it was
		// before any verifier could report one.
		jobs, err := a.d.Store.OpenJobsPage(r.Context(), capability, q.Get("peerId"), reason,
			q.Get("containerOs"), pageSize, offset)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "job listing failed")
			return
		}
		for _, j := range jobs {
			if a.d.Blobs == nil {
				continue
			}
			have, err := a.d.Blobs.Has(r.Context(), j.SampleID)
			if err != nil || !have {
				continue
			}
			jo := jobOut{ID: j.ID, SampleID: j.SampleID, Reason: j.Reason}
			if j.WantEnvJSON != "" {
				jo.WantEnv = json.RawMessage(j.WantEnvJSON)
			}
			out = append(out, jo)
			if len(out) >= limit {
				break
			}
		}
		offset += len(jobs)
		if len(jobs) < pageSize {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// handleJobClaim implements POST /v1/verification/jobs/{id}/claim {peerId}.
func (a *api) handleJobClaim(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid job id")
		return
	}
	var body struct {
		PeerID string `json:"peerId"`
	}
	if !readJSON(w, r, 64<<10, &body) {
		return
	}
	if !validPeerID(body.PeerID) {
		writeErr(w, http.StatusBadRequest, "peerId must be \"ed25519:<16 hex>\"")
		return
	}
	claimed, err := a.d.Store.ClaimJob(r.Context(), id, body.PeerID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "claim failed")
		return
	}
	if !claimed {
		writeErr(w, http.StatusConflict, "job already claimed or not open")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "claimed", "id": id})
}

// validPeerID checks the "ed25519:<16 lowercase hex>" fingerprint form.
func validPeerID(peerID string) bool {
	const prefix = "ed25519:"
	if len(peerID) != len(prefix)+16 || peerID[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(peerID); i++ {
		c := peerID[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// RecomputeStatus re-derives a sample's status from its receipts under the
// CURRENT rules, starting from PUBLISHED rather than from whatever the row
// already says.
//
// The live path only ever upgrades, which is right while the rules hold
// still. When a rule is corrected — as the environment-diversity gate was,
// after receipts that misstated where they ran had already granted
// MATRIX_PASS — a status earned under the old rule is simply wrong, and
// leaving it in place would advertise verification the evidence no longer
// supports. Downgrading is therefore an honesty operation, deliberately
// kept out of the request path and run by an operator.
func RecomputeStatus(rows []serverstore.ReceiptRow, now time.Time) string {
	return sampleStatusFromReceipts("PUBLISHED", rows, now)
}
