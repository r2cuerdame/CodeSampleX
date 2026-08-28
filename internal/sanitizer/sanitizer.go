// Package sanitizer turns raw tool output into privacy-safe error templates
// (goal.md §8.5, contract C11). The strip order is binding: (1) error codes
// are extracted and preserved, (2) public package names after node_modules/
// segments are kept, then (3) paths, (4) URLs, (5) emails, (6) quoted string
// literals, (7) hex/base64 token runs, (8) the current user and home dir, and
// (9) line/column numbers are replaced with placeholders. Raw input never
// leaves this package unsanitized.
package sanitizer

import (
	"os"
	"os/user"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// SanitizedError is the uploadable form of one raw error text.
type SanitizedError struct {
	Template      string
	Code          string
	Fingerprint   string
	PublicSymbols []string
}

var (
	// Error-code classes in Code-extraction priority order.
	reTSCode    = regexp.MustCompile(`\bTS\d{4,5}\b`)
	reNodeCode  = regexp.MustCompile(`\bERR_[A-Z_]+\b`)
	reRustCode  = regexp.MustCompile(`\bE\d{4}\b`)
	reErrnoCode = regexp.MustCompile(`\bE[A-Z]{2,}\d*\b`)
	codeClasses = []*regexp.Regexp{reTSCode, reNodeCode, reRustCode, reErrnoCode}

	reExitCode = regexp.MustCompile(`(?i)\b(exit code\s+)\d+\b`)
	reUUID     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	reTime     = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.]+(?:Z|[+-]\d{2}:?\d{2})?\b`)
	rePID      = regexp.MustCompile(`(?i)\b(pid\s*[=:]?\s*)\d+\b`)
	reIPv4Port = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}:\d{2,5}\b`)

	// node_modules paths: consume the whole path token, capture the trailing
	// package name (scoped alternative first so "@scope/name" wins).
	reNodeModules = regexp.MustCompile(`(?:[A-Za-z]:)?[^\s"':]*node_modules[\\/](@[^\\/\s"':]+[\\/][^\\/\s"':]+|[^\\/\s"':]+)[^\s"':]*`)

	// Path regexes carry a boundary guard so drive letters inside words and
	// slashes inside URLs (preceded by ':' or '/') are not misread as paths.
	reWinPath  = regexp.MustCompile(`(?m)(^|[\s"'(\[=])([A-Za-z]:[\\/][^\s"']+)`)
	reUnixPath = regexp.MustCompile(`(?m)(^|[\s"'(\[=])(/[^\s"':]+)`)
	reRelPath  = regexp.MustCompile(`\.{1,2}[\\/][^\s"']+`)

	reURL    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.\-]*://[^\s"')\]>]+`)
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reDQuote = regexp.MustCompile(`"[^"\n]*"`)
	reSQuote = regexp.MustCompile(`'[^'\n]*'`)

	reTokenCand = regexp.MustCompile(`[A-Za-z0-9+/=]{20,}`)
	reHexOnly   = regexp.MustCompile(`^[0-9a-fA-F]+$`)

	reParenLineCol = regexp.MustCompile(`\(\d+,\d+\)`)
	reColonNum     = regexp.MustCompile(`:\d+\b`)
)

// protector shields substrings that must survive the destructive steps.
type protector struct{ vals []string }

func (p *protector) add(v string) string {
	p.vals = append(p.vals, v)
	return "\x00" + strconv.Itoa(len(p.vals)-1) + "\x00"
}

func (p *protector) restore(s string) string {
	for i, v := range p.vals {
		s = strings.ReplaceAll(s, "\x00"+strconv.Itoa(i)+"\x00", v)
	}
	return s
}

// Sanitize strips all identifying material from raw per C11 and returns the
// template, the dominant error code, a stable fingerprint, and the public
// packages mentioned in raw.
func Sanitize(raw string, stage domain.Stage, publicPkgs []string) SanitizedError {
	return sanitize(raw, stage, publicPkgs, true)
}

// sanitize performs the deterministic sanitizer pipeline. scrubHostUser is
// true only at the producing machine's raw-log boundary. A server validating
// an already-normalized wire summary must not apply its own account name: the
// production container runs as "csx", which is also a legitimate substring
// of public package and module names.
func sanitize(raw string, stage domain.Stage, publicPkgs []string, scrubHostUser bool) SanitizedError {
	code := extractCode(raw)

	pub := make(map[string]bool, len(publicPkgs))
	for _, pkg := range publicPkgs {
		pub[pkg] = true
	}

	p := &protector{}
	s := raw

	// (1) Preserve error codes; exit-code numbers become <n>.
	for _, re := range codeClasses {
		s = re.ReplaceAllStringFunc(s, p.add)
	}
	s = reExitCode.ReplaceAllString(s, "${1}<n>")

	// (2) node_modules paths: keep only the public package name.
	s = reNodeModules.ReplaceAllStringFunc(s, func(m string) string {
		sub := reNodeModules.FindStringSubmatch(m)
		name := strings.ReplaceAll(sub[1], `\`, "/")
		if pub[name] {
			return p.add("node_modules/" + name)
		}
		return "<path>"
	})

	// (3) Absolute and relative paths.
	//
	// The boundary guard must be PUT BACK. Replacing the whole match with
	// "<path>" swallowed the delimiter, and when that delimiter was a quote
	// the remaining quotes re-paired against the wrong partners: for
	//
	//   open "C:\...\config.json" failed; password "hunter2pass" rejected
	//
	// step (3) left `open <path>" failed; password "hunter2pass" rejected`,
	// so step (6) matched `" failed; password "` as the quoted literal and
	// the PASSWORD survived into the template — which is returned to the
	// agent, and hashed into the fingerprint that gets uploaded. The unix
	// branch already restored it; this one did not.
	s = reWinPath.ReplaceAllString(s, "${1}<path>")
	s = reUnixPath.ReplaceAllString(s, "${1}<path>")
	s = reRelPath.ReplaceAllString(s, "<path>")

	// (4) URLs, (5) emails, (6) quoted string literals.
	s = reURL.ReplaceAllString(s, "<url>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reDQuote.ReplaceAllString(s, "<str>")
	s = reSQuote.ReplaceAllString(s, "<str>")

	// (7) Hex/base64 runs.
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reTime.ReplaceAllString(s, "<timestamp>")
	s = rePID.ReplaceAllString(s, "${1}<pid>")
	s = reIPv4Port.ReplaceAllString(s, "<ip>:<port>")
	s = reTokenCand.ReplaceAllStringFunc(s, func(m string) string {
		if tokenish(m) {
			return "<token>"
		}
		return m
	})

	// (8) Current user and home dir. This is producer-local secret removal,
	// not a portable canonicalization rule.
	if scrubHostUser {
		for _, re := range userScrubPatterns() {
			s = re.ReplaceAllString(s, "<user>")
		}
	}

	// (9) Line/column numbers.
	s = reParenLineCol.ReplaceAllString(s, "(<n>,<n>)")
	s = reColonNum.ReplaceAllString(s, ":<n>")

	s = p.restore(s)

	return SanitizedError{
		Template:      s,
		Code:          code,
		Fingerprint:   domain.SHA256Hex([]byte("v1|" + string(stage) + "|" + code + "|" + s)),
		PublicSymbols: mentioned(raw, publicPkgs),
	}
}

// SanitizeFailure builds the public v2 failure contract. The fingerprint is
// intentionally the cluster identity only: package/version scope and exact
// environment variants are stored beside it by aggregation, so an otherwise
// identical failure does not split merely because it ran on another machine.
func SanitizeFailure(raw string, stage domain.Stage, term domain.FailureTermination, _ []string) domain.FailureEvidence {
	term = term.Canonical()
	// Public FailureEvidence must be canonical without trusting producer-only
	// allowlists. Package identity already travels separately as structured
	// PURLs/receipt data, so node_modules paths are normalized here.
	san := Sanitize(raw, stage, nil)
	summary := PublicErrorSummary(san.Template)
	quality := domain.EvidenceMissing
	structured := term.Structured()
	switch {
	case structured && summary != "":
		quality = domain.EvidenceComplete
	case structured || summary != "":
		quality = domain.EvidencePartial
	}
	f := domain.FailureEvidence{
		TerminationKind: term.Kind,
		ExitCode:        term.ExitCode,
		Signal:          strings.ToUpper(strings.TrimSpace(term.Signal)),
		TimeoutMillis:   term.TimeoutMillis,
		ErrorSummary:    summary,
		ErrorCode:       san.Code,
		EvidenceQuality: quality,
	}
	if quality != domain.EvidenceMissing {
		f.Fingerprint = domain.FailureFingerprint(stage, term, san.Code, summary)
	}
	return f
}

// SanitizeClassifiedFailure builds public evidence for one actual failure
// event while preserving the privacy-bounded outer-to-toolchain lineage.
func SanitizeClassifiedFailure(raw string, stage domain.Stage, term domain.FailureTermination, _ []string,
	outerCommand string, outerStage domain.Stage, actualToolchain string,
	stageEvidence domain.FailureStageEvidence, evidenceGap domain.FailureEvidenceGap) domain.FailureEvidence {
	f := SanitizeFailure(raw, stage, term, nil)
	f.OuterCommand = outerCommand
	f.OuterStage = outerStage
	f.ActualToolchain = strings.ToLower(strings.TrimSpace(actualToolchain))
	f.StageEvidence = stageEvidence
	f.EvidenceGap = evidenceGap
	if f.Fingerprint != "" && f.ActualToolchain != "" {
		f.Fingerprint = domain.ClassifiedFailureFingerprint(stage, f.ActualToolchain, f.Termination(), f.ErrorCode, f.ErrorSummary)
	}
	return f
}

// PublicErrorSummary keeps the first useful normalized lines within a strict
// public-wire cap. It is idempotent, which lets the server reject any client
// value that was not produced by the same canonical normalization.
func PublicErrorSummary(normalized string) string {
	const maxBytes = 512
	lines := strings.Split(strings.ReplaceAll(normalized, "\r\n", "\n"), "\n")
	out := make([]string, 0, 4)
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) == 4 {
			break
		}
	}
	s := strings.Join(out, " · ")
	if len(s) > maxBytes {
		s = s[:maxBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s = strings.TrimSpace(s)
	}
	return s
}

// CanonicalPublicErrorSummary revalidates a normalized public-wire summary
// without introducing facts from the validating host. Raw producers still
// use SanitizeFailure, which removes their own username and home directory.
func CanonicalPublicErrorSummary(summary string, stage domain.Stage) string {
	return PublicErrorSummary(sanitize(summary, stage, nil, false).Template)
}

// observationStages are the stages an error can actually be RECORDED
// under, and therefore the only stages a stored fingerprint can carry.
var observationStages = []domain.Stage{
	domain.StageUsed,
	domain.StageProjectResolve,
	domain.StageProjectTypecheck,
	domain.StageProjectCompile,
	domain.StageProjectTest,
	domain.StageProjectProcess,
	domain.StageProjectLoad,
	domain.StageProcessStart,
	domain.StageUnknown,
}

// Fingerprints returns the fingerprint this error would carry at each stage
// it could have been recorded under.
//
// A fingerprint is SHA256("v1|" + stage + "|" + code + "|" + template), so
// the same error text hashes differently depending on the stage that
// observed it. Everything that RECORDS an error knows its stage. The agent
// pasting an error into search_known_solution does not -- it has a build
// log, not a stage -- and searching with an empty stage produced a hash
// that could not equal any stored fingerprint, ever, on any install. That
// silently disabled the largest relevance weight in the ranker (0.60,
// larger than an exact version match at 0.45) and, with it, the rule that
// exempts "the failure the caller is explicitly looking for a fix to" from
// being demoted to REFERENCE_ONLY.
//
// Asking about every observation stage is the honest form of the question: the caller has
// this error, and does not claim to know when it happened.
func (s SanitizedError) Fingerprints() []string {
	out := make([]string, 0, len(observationStages))
	for _, stage := range observationStages {
		out = append(out, domain.SHA256Hex(
			[]byte("v1|"+string(stage)+"|"+s.Code+"|"+s.Template)))
	}
	return out
}

// ErrorCode returns the dominant error code in raw, or "" when it carries
// none.
//
// It is the cheap half of Sanitize, exported because choosing WHICH of a
// failed command's two streams to sanitize has to happen before sanitizing
// one, and that choice needs the code and nothing else.
func ErrorCode(raw string) string { return extractCode(raw) }

func extractCode(raw string) string {
	for _, re := range codeClasses {
		if m := re.FindString(raw); m != "" {
			return m
		}
	}
	return ""
}

// tokenish reports whether a candidate run looks like a secret/hash rather
// than a long word: pure hex, or base64-shaped (digit plus mixed case or
// base64 symbols).
func tokenish(m string) bool {
	if reHexOnly.MatchString(m) {
		return true
	}
	var hasDigit, hasUpper, hasLower, hasSym bool
	for _, r := range m {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		default:
			hasSym = true
		}
	}
	// A digit is the signal, once a run is this long.
	//
	// The rule used to be hasDigit && ((hasUpper && hasLower) || hasSym),
	// so an all-lowercase key with digits in it — a very ordinary shape for
	// an API key, a session id or a base36 token — passed through into a
	// field named "sanitized" and was handed back to the caller.
	//
	// Widening it is safe because the candidate pattern already requires 20
	// characters: sha256, utf8, base64 and node22 are never candidates. What
	// it can now catch that it should not is a very long identifier with a
	// digit in it, and losing one of those costs nothing — public symbols
	// are extracted separately and are not taken from this text. This is the
	// one place where saying less is unambiguously the safe direction.
	return hasDigit && (hasLower || hasUpper || hasSym)
}

func mentioned(raw string, publicPkgs []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, pkg := range publicPkgs {
		if pkg != "" && !seen[pkg] && strings.Contains(raw, pkg) {
			seen[pkg] = true
			out = append(out, pkg)
		}
	}
	sort.Strings(out)
	return out
}

var (
	userOnce     sync.Once
	userPatterns []*regexp.Regexp
)

// userScrubPatterns compiles case-insensitive patterns for the home dir and
// username once. Values shorter than 3 chars are skipped: replacing them
// would mangle ordinary words more than it would protect anyone.
func userScrubPatterns() []*regexp.Regexp {
	userOnce.Do(func() {
		var vals []string
		if home, err := os.UserHomeDir(); err == nil && len(home) >= 3 {
			vals = append(vals, home)
		}
		if name := currentUsername(); len(name) >= 3 {
			vals = append(vals, name)
		}
		for _, v := range vals {
			userPatterns = append(userPatterns, regexp.MustCompile(`(?i)`+regexp.QuoteMeta(v)))
		}
	})
	return userPatterns
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		name := u.Username
		// Windows reports DOMAIN\name.
		if i := strings.LastIndexAny(name, `\/`); i >= 0 {
			name = name[i+1:]
		}
		return name
	}
	if v := os.Getenv("USERNAME"); v != "" {
		return v
	}
	return os.Getenv("USER")
}

// Redact strips identifying material from a short free-text field and says
// whether it had to.
//
// Sanitize is for tool OUTPUT: it needs a stage, it preserves error codes and
// public package names, and it produces a fingerprint. An anomaly report's
// prose fields are neither output nor evidence — they are a sentence a human
// will read in a queue — and they arrive from a language model that was asked
// not to include a path and may have included one anyway.
//
// So this applies only the destructive half, in the same order and with the
// same expressions, and reports the fact of redaction. Both ends run it: the
// client so nothing identifying is ever sent, the server because the client
// is a program somebody else can replace.
func Redact(raw string) (clean string, redacted bool) {
	s := raw
	s = reNodeModules.ReplaceAllString(s, "<path>")
	s = reWinPath.ReplaceAllString(s, "${1}<path>")
	s = reUnixPath.ReplaceAllString(s, "${1}<path>")
	s = reRelPath.ReplaceAllString(s, "<path>")
	s = reURL.ReplaceAllString(s, "<url>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reDQuote.ReplaceAllString(s, "<str>")
	s = reSQuote.ReplaceAllString(s, "<str>")
	s = reTokenCand.ReplaceAllStringFunc(s, func(m string) string {
		if tokenish(m) {
			return "<token>"
		}
		return m
	})
	for _, re := range userScrubPatterns() {
		s = re.ReplaceAllString(s, "<user>")
	}
	return s, s != raw
}
