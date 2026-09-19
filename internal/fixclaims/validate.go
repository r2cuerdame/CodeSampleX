package fixclaims

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Rejection is one deterministic reason a candidate may not enter the
// queue. Code is the stable machine-readable half; Reason is for a person.
type Rejection struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

func (r Rejection) Error() string { return r.Code + ": " + r.Reason }

// Rejection codes. They are part of the ingest wire contract and are added
// to rather than reworded.
const (
	RejectSchema         = "invalid-schema"
	RejectEcosystem      = "unknown-ecosystem"
	RejectName           = "invalid-name"
	RejectVersion        = "invalid-version"
	RejectVersionOrder   = "versions-not-ordered"
	RejectClaim          = "invalid-claim"
	RejectSourceURL      = "invalid-source-url"
	RejectSourceType     = "invalid-source-type"
	RejectReference      = "invalid-reference"
	RejectSymbol         = "invalid-symbol"
	RejectHint           = "invalid-environment-hint"
	RejectConfidence     = "invalid-confidence"
	RejectFingerprint    = "invalid-fingerprint-hint"
	RejectNonExecutable  = "non-executable-claim"
	RejectNoTarget       = "no-executable-target"
	RejectTooManySymbols = "too-many-symbols"
)

const (
	minClaimLen     = 12
	maxClaimLen     = 400
	maxSourceURLLen = 512
	maxSymbols      = 8
	maxHints        = 8
	maxReferences   = 8
)

var (
	symbolToken = regexp.MustCompile(`^[A-Za-z_$@][A-Za-z0-9_$@.:/#\-<>\[\]()]{0,119}$`)
	hintToken   = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,31}$`)
	hexDigest   = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// nonExecutable are the words a docs-only, cosmetic or process line
	// carries. A claim that names one and nothing a contract could observe
	// is refused: there is no behaviour to run on either side of the
	// boundary.
	nonExecutable = regexp.MustCompile(`(?i)\b(typos?|readme|docs?|documentation|docstrings?|changelog|licen[cs]e|spelling|wording|grammar|formatting|lint(ing|er)?|whitespace|prettier|ci|github actions?|workflow|badge|dependabot|bump(ed|s)?|changesets?|release notes?|comments?|jsdoc|typing hints?)\b`)
	// docsOnly are the marks of a line about documentation, tooling or
	// process, refused whatever else it says: a fix to a ".md" file, a
	// conventional-commit scope of docs/test/chore/ci/lint/build, a release
	// summary, a dependency upgrade. Measured on the Phase 0 corpus
	// (#444): "fix reversed poll order in timeout doc" and "Fix broken
	// links in active_help.md" both carry an executable word and neither
	// is a behaviour.
	docsOnly = regexp.MustCompile(`(?i)(\.md\b|\bdocs?\b|\bdocumentation\b|\btypos?\b|\breadme\b|\bchangelog\b|\bjsdoc\b|\bdocstrings?\b|\blinks?\b|\bwarning about\b|^(chore|ci|tests?|docs?|lint|style|build|refactor|perf|deps)(\([^)]*\))?:|^fix\((tests?|ci|docs?|lint|build|deps|types?)\):|^(documentation|developer experience|dependencies|chores?|tests?|tooling|internal)\s*:|^this release\b|\bupgrade[sd]? \S+ to v?\d|\btest cleanup\b|\bin tests?\b)`)
	// executable are the words that describe something a contract can
	// assert: a crash, an error, a wrong result, a hang, a regression.
	executable = regexp.MustCompile(`(?i)\b(crash(es|ed|ing)?|panics?|exceptions?|throws?|thrown|errors?|hangs?|hung|deadlocks?|leaks?|leaking|incorrect(ly)?|wrong(ly)?|regressions?|fails?|failed|failing|failure|broken|breaks?|timeouts?|race|corrupt(s|ed|ion)?|invalid|null|undefined|nan|overflow|segfault|infinite loop|not work(ing)?|does not|doesn't|no longer|memory|unexpected(ly)?|mismatch|missing|ignored|lost|duplicate[ds]?|encoding|pars(e|ing)|return(s|ed|ing)?|resolv(e|es|ed|ing)|serializ|deserializ|escap(e|es|ed|ing)|truncat|stack overflow|infinite|freeze|blocked|silently|drops?|dropped|honou?r(s|ed)?|respect(s|ed)?|stalls?|aborts?|cancel(s|led|ed)?|propagat|underflow|unbounded|inert|reject(s|ed)?|prevent(s|ed)?|times? out|refused|unbound|destroy(s|ed)?|orphan(s|ed)?|never settles?|unhandled)\b`)
)

// Validate applies every deterministic rule to a candidate and returns
// every rejection it earns, so a producer can fix all of them in one round
// rather than discovering them one resubmission at a time. The candidate is
// normalized first; callers store the normalized form.
//
// Nothing here consults a registry, a model or the network. The point of
// the validator is that its answer is the same on every machine and on
// every day, which is what makes AGY output ingestable without trusting it.
func Validate(c Candidate) (Candidate, []Rejection) {
	c = c.Normalized()
	var out []Rejection
	add := func(code, format string, args ...any) {
		out = append(out, Rejection{Code: code, Reason: fmt.Sprintf(format, args...)})
	}
	if c.SchemaVersion != CandidateSchemaVersion {
		add(RejectSchema, "schemaVersion must be %d", CandidateSchemaVersion)
	}
	if !domain.AllowedEcosystems[c.Ecosystem] {
		add(RejectEcosystem, "ecosystem %q is not one this network verifies", c.Ecosystem)
	}
	if c.Name == "" || len(c.Name) > 214 || strings.ContainsAny(c.Name, " \t\r\n") {
		add(RejectName, "name must be a public package name")
	} else if _, err := domain.ParsePURL(domain.PURL{Ecosystem: c.Ecosystem, Name: c.Name, Version: "0"}.String()); err != nil && domain.AllowedEcosystems[c.Ecosystem] {
		add(RejectName, "name does not form a valid purl: %v", err)
	}
	if c.ClaimedFixedVersion == "" {
		add(RejectVersion, "claimedFixedVersion is required")
	} else if !domain.ConcreteResolvedVersion(c.ClaimedFixedVersion) {
		add(RejectVersion, "claimedFixedVersion %q is not a concrete release", c.ClaimedFixedVersion)
	}
	if c.ClaimedBadVersion != "" {
		if !domain.ConcreteResolvedVersion(c.ClaimedBadVersion) {
			add(RejectVersion, "claimedBadVersion %q is not a concrete release", c.ClaimedBadVersion)
		} else if c.ClaimedFixedVersion != "" && domain.CompareVersions(c.ClaimedBadVersion, c.ClaimedFixedVersion) >= 0 {
			add(RejectVersionOrder, "claimedBadVersion %q must precede claimedFixedVersion %q", c.ClaimedBadVersion, c.ClaimedFixedVersion)
		}
	}
	if n := len(c.Claim); n < minClaimLen || n > maxClaimLen {
		add(RejectClaim, "claim must be %d..%d characters, got %d", minClaimLen, maxClaimLen, n)
	}
	if !validHTTPSURL(c.SourceURL) {
		add(RejectSourceURL, "sourceUrl must be an https URL of at most %d characters", maxSourceURLLen)
	}
	switch c.SourceType {
	case SourceReleaseNote, SourceIssue, SourcePR, SourceChangelog:
	default:
		add(RejectSourceType, "sourceType must be one of %s", strings.Join(SourceTypes(), ", "))
	}
	if len(c.References) > maxReferences {
		add(RejectReference, "at most %d references", maxReferences)
	}
	for _, ref := range c.References {
		if !validHTTPSURL(ref) {
			add(RejectReference, "reference %q must be an https URL", ref)
			break
		}
	}
	if len(c.Symbols) > maxSymbols {
		add(RejectTooManySymbols, "at most %d symbols; a claim about everything is not a claim about a behaviour", maxSymbols)
	}
	for _, s := range c.Symbols {
		if !symbolToken.MatchString(s) {
			add(RejectSymbol, "symbol %q is not an identifier", s)
			break
		}
	}
	if len(c.EnvironmentHints) > maxHints {
		add(RejectHint, "at most %d environment hints", maxHints)
	}
	for _, h := range c.EnvironmentHints {
		if !hintToken.MatchString(h) {
			add(RejectHint, "environment hint %q is not a lowercase token", h)
			break
		}
	}
	switch c.Confidence {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
	default:
		add(RejectConfidence, "confidence must be high, medium or low")
	}
	if c.FailureFingerprintHint != "" && !hexDigest.MatchString(c.FailureFingerprintHint) {
		add(RejectFingerprint, "failureFingerprintHint must be a 64-hex digest")
	}
	if len(c.Claim) >= minClaimLen {
		exec := executable.MatchString(c.Claim)
		switch {
		case docsOnly.MatchString(c.Claim), nonExecutable.MatchString(c.Claim) && !exec:
			add(RejectNonExecutable, "claim describes documentation, formatting or process, not a behaviour a contract can run")
		case !exec && len(c.Symbols) == 0:
			add(RejectNoTarget, "claim names neither a symbol nor an observable failure")
		}
	}
	return c, out
}

func validHTTPSURL(raw string) bool {
	if raw == "" || len(raw) > maxSourceURLLen {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return false
	}
	return true
}

// Executable reports whether a claim line reads as a behaviour a contract
// can assert: it names a failure word and is not about documentation,
// tooling or process.
func Executable(claim string) bool {
	return executable.MatchString(claim) && !docsOnly.MatchString(claim)
}
