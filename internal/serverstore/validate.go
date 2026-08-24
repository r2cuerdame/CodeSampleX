package serverstore

import (
	"fmt"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// maxObservationCount caps a single batch row's claimed count. Local clients
// sync at least every 15 minutes, so honest epoch totals stay far below this;
// anything bigger is abuse or a bug and is rejected outright.
const maxObservationCount = 1_000_000

// observationStages are the only stages the anonymous evidence path accepts
// (docs/execution-context.md §3). Verification stages (CONTRACT etc.) arrive
// via signed receipts, never via observation batches.
var observationStages = map[domain.Stage]bool{
	domain.StageUsed:             true,
	domain.StageProjectTypecheck: true,
	domain.StageProjectCompile:   true,
	domain.StageProjectTest:      true,
	domain.StageProjectLoad:      true,
	domain.StageProjectProcess:   true,
}

// ValidateBatch checks one ObservationBatch against the wire contract before
// it may touch storage. It is pure and shared with the HTTP ingest handler.
//
// Enforced here (binding):
//   - PURL parses and its ecosystem is on the public allowlist (§8.1).
//   - Stage is an observation stage. SYMBOL_EXECUTED / SYMBOL_CALL are
//     A3-only runtime-instrumentation stages and no Public v1 adapter claims
//     A3, so they are rejected (docs/execution-context.md §3).
//   - symbolConfidence is EXACT, PROBABLE or UNKNOWN (empty allowed).
//   - errorFingerprint must be a sha256 content id — raw error text can
//     never ride in on that field.
func ValidateBatch(b domain.ObservationBatch) error {
	if b.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1, got %d", b.SchemaVersion)
	}
	if err := validEpoch(b.Epoch); err != nil {
		return err
	}
	if err := validBucket("anonId", b.AnonID); err != nil {
		return err
	}
	if err := validBucket("projectBucket", b.ProjectBucket); err != nil {
		return err
	}
	p, err := domain.ParsePURL(b.Package)
	if err != nil {
		return fmt.Errorf("bad purl: %v", err)
	}
	if !domain.AllowedEcosystems[p.Ecosystem] {
		return fmt.Errorf("ecosystem %q not in the public allowlist", p.Ecosystem)
	}
	switch b.Stage {
	case domain.StageSymbolExecuted, domain.StageSymbolCall:
		return fmt.Errorf("stage %s is A3-only runtime instrumentation; no Public v1 adapter claims A3", b.Stage)
	}
	if !observationStages[b.Stage] {
		return fmt.Errorf("stage %q is not an observation stage", b.Stage)
	}
	if b.Result != domain.ResultPass && b.Result != domain.ResultFail {
		return fmt.Errorf("result must be PASS or FAIL, got %q", b.Result)
	}
	switch b.SymbolConfidence {
	case "", domain.SymbolExact, domain.SymbolProbable, domain.SymbolUnknown:
	default:
		return fmt.Errorf("symbolConfidence %q is not EXACT, PROBABLE or UNKNOWN", b.SymbolConfidence)
	}
	if len(b.Symbol) > 512 {
		return fmt.Errorf("symbol longer than 512 bytes")
	}
	if b.ObservationCount < 1 || b.ObservationCount > maxObservationCount {
		return fmt.Errorf("observationCount %d outside 1..%d", b.ObservationCount, maxObservationCount)
	}
	if b.ErrorFingerprint != "" && !validSHA256ID(b.ErrorFingerprint) {
		return fmt.Errorf("errorFingerprint must be \"sha256:<64 hex>\"")
	}
	if err := validErrorCode(b.ErrorCode); err != nil {
		return err
	}
	if err := validFailureEvidence(b, p.Name); err != nil {
		return err
	}
	if err := validEnv(b.Environment); err != nil {
		return err
	}
	if err := validCoresident(b.Coresident); err != nil {
		return err
	}
	if err := validDependsOn(p, b.DependsOn); err != nil {
		return err
	}
	return nil
}

func normalizedEvidenceQuality(b domain.ObservationBatch) string {
	if b.Result != domain.ResultFail {
		return ""
	}
	if b.EvidenceQuality == "" {
		return string(domain.EvidenceLegacyIncomplete)
	}
	return string(b.EvidenceQuality)
}

func validFailureEvidence(b domain.ObservationBatch, publicPackage string) error {
	if b.Result == domain.ResultPass {
		if b.TerminationKind != "" || b.ExitCode != nil || b.Signal != "" || b.TimeoutMillis != 0 ||
			b.ErrorSummary != "" || b.EvidenceQuality != "" || b.ErrorFingerprint != "" || b.ErrorCode != "" {
			return fmt.Errorf("PASS must not carry failure evidence")
		}
		return nil
	}
	switch b.EvidenceQuality {
	case "", domain.EvidenceComplete, domain.EvidencePartial, domain.EvidenceMissing, domain.EvidenceLegacyIncomplete:
	default:
		return fmt.Errorf("evidenceQuality %q is invalid", b.EvidenceQuality)
	}
	term := domain.FailureTermination{Kind: b.TerminationKind, ExitCode: b.ExitCode, Signal: b.Signal, TimeoutMillis: b.TimeoutMillis}
	if b.TerminationKind != "" && !term.Structured() {
		return fmt.Errorf("terminationKind %q lacks its required structured value", b.TerminationKind)
	}
	if b.ExitCode != nil && b.TerminationKind != domain.TerminationExit {
		return fmt.Errorf("exitCode requires terminationKind exit")
	}
	if b.Signal != "" && b.TerminationKind != domain.TerminationSignal {
		return fmt.Errorf("signal requires terminationKind signal")
	}
	if len(b.Signal) > 32 {
		return fmt.Errorf("signal longer than 32 bytes")
	}
	if b.TimeoutMillis < 0 || (b.TimeoutMillis > 0 && b.TerminationKind != domain.TerminationTimeout) {
		return fmt.Errorf("timeoutMillis requires terminationKind timeout and cannot be negative")
	}
	if len(b.ErrorSummary) > 512 {
		return fmt.Errorf("errorSummary longer than 512 bytes")
	}
	if b.ErrorSummary != "" {
		// Producers intentionally preserve a public package token from
		// node_modules/<name>. Revalidation must use the same allowlist or a
		// producer-canonical summary becomes <path> here and is rejected.
		publicNames := []string(nil)
		if publicPackage != "" {
			publicNames = []string{publicPackage}
		}
		canonical := sanitizer.PublicErrorSummary(sanitizer.Sanitize(b.ErrorSummary, b.Stage, publicNames).Template)
		if canonical != b.ErrorSummary {
			return fmt.Errorf("errorSummary is not canonical secret-safe normalized text")
		}
	}
	if b.EvidenceQuality == domain.EvidenceMissing && (term.Structured() || b.ErrorSummary != "" || b.ErrorFingerprint != "" || b.ErrorCode != "") {
		return fmt.Errorf("evidenceQuality missing cannot carry inferred failure evidence")
	}
	if b.EvidenceQuality == domain.EvidenceComplete && (!term.Structured() || b.ErrorSummary == "" || b.ErrorFingerprint == "") {
		return fmt.Errorf("evidenceQuality complete requires termination, normalized error and fingerprint")
	}
	if b.EvidenceQuality == domain.EvidencePartial {
		hasTermination, hasSummary := term.Structured(), b.ErrorSummary != ""
		if hasTermination == hasSummary || b.ErrorFingerprint == "" {
			return fmt.Errorf("evidenceQuality partial requires exactly one of termination or normalized error, plus fingerprint")
		}
	}
	if b.EvidenceQuality == domain.EvidenceComplete || b.EvidenceQuality == domain.EvidencePartial {
		// Modern clients do not get to choose the cluster identity. The
		// server recomputes the documented v2 coordinate from the structured
		// evidence so a buggy or hostile client cannot split identical failures
		// or merge unrelated ones with an arbitrary syntactically-valid SHA.
		expected := domain.SHA256Hex([]byte("v2|" + string(b.Stage) + "|" + term.FingerprintCoordinate() + "|" + b.ErrorCode + "|" + b.ErrorSummary))
		if b.ErrorFingerprint != expected {
			return fmt.Errorf("errorFingerprint does not match structured failure evidence")
		}
	}
	return nil
}

// A batch's edge facts turn into one INSERT each, and their strings land in
// indexed columns, so both need the caps ObservationCount already has. The
// count caps are the shared wire contract (the client clamps to them before
// sending); the byte caps stop an oversized string aborting the whole batch's
// transaction at the index instead of being refused here.
const (
	maxCoresidentPerBatch = domain.MaxCoresidentPerBatch
	maxDependsOnPerBatch  = domain.MaxDependsOnPerBatch
	maxCoresidentBytes    = 64
	maxDependsOnBytes     = 512
)

// validCoresident checks the other-version list: each entry must have the
// shape of a resolver-selected release. A range, a URL or free text is not a
// version anything installed, and an oversized string would abort the whole
// batch's transaction at the index instead of being refused here.
func validCoresident(versions []string) error {
	if len(versions) > maxCoresidentPerBatch {
		return fmt.Errorf("coresident lists %d versions, max %d", len(versions), maxCoresidentPerBatch)
	}
	for _, v := range versions {
		if len(v) > maxCoresidentBytes {
			return fmt.Errorf("coresident version longer than %d bytes", maxCoresidentBytes)
		}
		if !domain.ConcreteResolvedVersion(v) {
			return fmt.Errorf("coresident version %q is not a resolved release", v)
		}
	}
	return nil
}

// validDependsOn checks the edge list: every child must be a parseable purl
// in the parent's own ecosystem, pinned to a resolved release. A lockfile
// never crosses an ecosystem, so an edge that claims to is a parse error
// wearing a fact's clothes.
func validDependsOn(parent domain.PURL, edges []string) error {
	if len(edges) > maxDependsOnPerBatch {
		return fmt.Errorf("dependsOn lists %d edges, max %d", len(edges), maxDependsOnPerBatch)
	}
	for _, raw := range edges {
		if len(raw) > maxDependsOnBytes {
			return fmt.Errorf("dependsOn entry longer than %d bytes", maxDependsOnBytes)
		}
		child, err := domain.ParsePURL(raw)
		if err != nil {
			return fmt.Errorf("dependsOn: bad purl: %v", err)
		}
		if child.Ecosystem != parent.Ecosystem {
			return fmt.Errorf("dependsOn edge crosses ecosystems (%q under %q)", child.Ecosystem, parent.Ecosystem)
		}
		if !domain.ConcreteResolvedVersion(child.Version) {
			return fmt.Errorf("dependsOn version %q is not a resolved release", child.Version)
		}
	}
	return nil
}

func validEpoch(epoch string) error {
	t, err := time.Parse("2006-01-02", epoch)
	if err != nil || t.Format("2006-01-02") != epoch {
		return fmt.Errorf("epoch %q is not a YYYY-MM-DD day bucket", epoch)
	}
	return nil
}

func validBucket(field, v string) error {
	if v == "" {
		return fmt.Errorf("%s is empty", field)
	}
	if len(v) > 64 {
		return fmt.Errorf("%s longer than 64 bytes", field)
	}
	for _, r := range v {
		if r <= ' ' || r > '~' {
			return fmt.Errorf("%s contains whitespace or non-printable characters", field)
		}
	}
	return nil
}

func validSHA256ID(s string) bool {
	hex, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(hex) != 64 {
		return false
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// validErrorCode accepts compact machine codes ("TS2345", "ERR_REQUIRE_ESM",
// "E0308") and rejects anything that looks like raw log text.
func validErrorCode(code string) error {
	if code == "" {
		return nil
	}
	if len(code) > 64 {
		return fmt.Errorf("errorCode longer than 64 bytes")
	}
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.', r == ':':
		default:
			return fmt.Errorf("errorCode contains %q — codes only, never raw error text", r)
		}
	}
	return nil
}

func validEnv(e domain.EnvironmentFingerprint) error {
	if e.SchemaVersion != 1 {
		return fmt.Errorf("environment.schemaVersion must be 1, got %d", e.SchemaVersion)
	}
	if e.Ecosystem == "" || e.OS == "" || e.Arch == "" {
		return fmt.Errorf("environment requires ecosystem, os and arch")
	}
	return nil
}
