// Package fixclaims turns upstream bug-fix claims -- a release note, a
// closed issue, a merged PR -- into version-bounded, executed evidence
// (#444).
//
// The distinction the whole package exists for: a maintainer saying a bug
// is fixed is provenance, an LLM restating it is a hypothesis, and only a
// contract that FAILED on the bad release and PASSED on the claimed-fixed
// release, with a signed receipt on each side, is evidence. Nothing in this
// package can mark a claim verified; only runs recorded against receipts
// move a candidate past CLAIMED_FIX, and the evaluator that does the moving
// (evaluate.go) is a pure function of those runs.
package fixclaims

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// CandidateSchemaVersion is the one schemaVersion the ingest accepts.
const CandidateSchemaVersion = 1

// SourceType is where a claim came from. It is provenance, kept verbatim on
// the record so a reader can follow it back; it is never evidence.
type SourceType string

const (
	SourceReleaseNote SourceType = "release_note"
	SourceIssue       SourceType = "issue"
	SourcePR          SourceType = "pr"
	SourceChangelog   SourceType = "changelog"
)

// SourceTypes lists the accepted source types in documentation order.
func SourceTypes() []string {
	return []string{string(SourceReleaseNote), string(SourceIssue), string(SourcePR), string(SourceChangelog)}
}

// Confidence is how sure the extractor is that the line names an executable
// bug fix. It orders the queue; it never decides a status.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Candidate is one FIX_CANDIDATE record: a normalized upstream claim with
// its provenance, and nothing else. It deliberately has no status field. A
// collector or an AGY lane produces these; the validator (validate.go)
// decides whether one may enter the queue; the queue hands it to Farm.
type Candidate struct {
	SchemaVersion int    `json:"schemaVersion"`
	Ecosystem     string `json:"ecosystem"`
	Name          string `json:"name"`
	// ClaimedBadVersion is the release the source says (or the collector
	// infers) still carried the bug. Optional: a release note names the fix
	// but rarely the last broken release, and the queue can fill it from
	// the release immediately below the fixed one.
	ClaimedBadVersion   string `json:"claimedBadVersion,omitempty"`
	ClaimedFixedVersion string `json:"claimedFixedVersion"`
	// Claim is the normalized short description of what the source says was
	// fixed. Bounded free text; it is shown as a quote, never parsed as
	// truth.
	Claim      string     `json:"claim"`
	SourceURL  string     `json:"sourceUrl"`
	SourceType SourceType `json:"sourceType"`
	// References are the issue and PR URLs the source line points at.
	References []string `json:"references,omitempty"`
	// Symbols are the APIs or features the claim is about, when the source
	// names them. A candidate with none must still name an executable
	// behaviour in its claim text or it is refused.
	Symbols []string `json:"symbols,omitempty"`
	// EnvironmentHints are lowercase tokens the source ties the bug to:
	// "windows", "python-3.14", "node-22". They steer the first probes and
	// nothing else.
	EnvironmentHints []string   `json:"environmentHints,omitempty"`
	Confidence       Confidence `json:"confidence"`
	// FailureFingerprintHint is a 64-hex CSX failure fingerprint the
	// extractor matched this claim against, when it did. A match means the
	// reproducer may already exist, which is why it weighs so heavily in
	// the queue score.
	FailureFingerprintHint string `json:"failureFingerprintHint,omitempty"`
	// UpstreamReproducer says the source itself carries a reproducer the
	// worker should adapt before writing one.
	UpstreamReproducer bool `json:"upstreamReproducer,omitempty"`
	// ReleasedAt is when the claimed-fixed release was published, for the
	// freshness term of the score. Optional.
	ReleasedAt time.Time `json:"releasedAt,omitempty"`
}

// Package is the purl of the claimed-fixed release.
func (c Candidate) Package() domain.PURL {
	return domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: c.ClaimedFixedVersion}
}

// BadPackage is the purl of the claimed-bad release, or the zero purl when
// the candidate does not name one.
func (c Candidate) BadPackage() domain.PURL {
	if c.ClaimedBadVersion == "" {
		return domain.PURL{}
	}
	return domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: c.ClaimedBadVersion}
}

var claimSpace = regexp.MustCompile(`\s+`)

// NormalizeClaim collapses whitespace and case so the same sentence copied
// from two sources dedupes to one candidate.
func NormalizeClaim(s string) string {
	return strings.ToLower(strings.TrimSpace(claimSpace.ReplaceAllString(s, " ")))
}

// Normalized returns the candidate with every coordinate in canonical form:
// lowercase ecosystem, canonical versions, deduplicated sorted symbols and
// hints, collapsed claim text. Validation runs on the normalized form.
func (c Candidate) Normalized() Candidate {
	c.Ecosystem = strings.ToLower(strings.TrimSpace(c.Ecosystem))
	c.Name = strings.TrimSpace(c.Name)
	c.ClaimedBadVersion = domain.CanonicalVersion(c.Ecosystem, strings.TrimSpace(c.ClaimedBadVersion))
	c.ClaimedFixedVersion = domain.CanonicalVersion(c.Ecosystem, strings.TrimSpace(c.ClaimedFixedVersion))
	c.Claim = strings.TrimSpace(claimSpace.ReplaceAllString(c.Claim, " "))
	c.SourceURL = strings.TrimSpace(c.SourceURL)
	c.SourceType = SourceType(strings.ToLower(strings.TrimSpace(string(c.SourceType))))
	c.Confidence = Confidence(strings.ToLower(strings.TrimSpace(string(c.Confidence))))
	c.FailureFingerprintHint = strings.ToLower(strings.TrimSpace(c.FailureFingerprintHint))
	c.References = dedupeSorted(c.References, false)
	c.Symbols = dedupeSorted(c.Symbols, false)
	c.EnvironmentHints = dedupeSorted(c.EnvironmentHints, true)
	c.ReleasedAt = c.ReleasedAt.UTC()
	if c.ReleasedAt.IsZero() {
		c.ReleasedAt = time.Time{}
	}
	return c
}

func dedupeSorted(in []string, lower bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if lower {
			s = strings.ToLower(s)
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// DedupKey is the identity of a candidate: the package, the release it
// claims fixed, and the normalized claim text. Two sources describing the
// same fix in the same release are one candidate; the same fix claimed for
// two releases is two, because each release needs its own pair of runs.
func (c Candidate) DedupKey() string {
	c = c.Normalized()
	sum := sha256.Sum256([]byte(c.Ecosystem + "\x00" + c.Name + "\x00" + c.ClaimedFixedVersion + "\x00" + NormalizeClaim(c.Claim)))
	return hex.EncodeToString(sum[:])
}
