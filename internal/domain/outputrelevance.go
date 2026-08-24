package domain

import (
	"regexp"
	"strings"

	searchrelevance "github.com/r2cuerdame/codesamplex/internal/search/relevance"
)

// The normal-output relevance gate.
//
// CodeSampleX is not a recommender. Normal MCP and hook output is a fact
// layer: the coordinate that ran, what its contract proved, what failed, and
// where the caller's environment differs from where it ran. A candidate that
// is not ABOUT the caller's case has no fact to contribute to it, and putting
// one in front of a model costs more than saying nothing — the model reads
// the body, not the REFERENCE_CANDIDATE label around it.
//
// The gate that already existed asked one question, "is this the wrong
// language", and it asked it from the failing command's argv. That misses the
// case this exists for. Asked how a GitHub Actions workflow_dispatch deploys
// an immutable main SHA, the network answered with FormatInteger/FormatFloat
// from github.com/dustin/go-humanize, graded COMPATIBLE. Same ecosystem, same
// Go minor, same arch — and not one package, symbol, error or goal word in
// common. Environment coordinates say where a sample can RUN. They never say
// what it is about, and they are exactly what an unrelated candidate shares.
//
// So the rule here is the inverse of a score: a candidate reaches normal
// output when at least one CONCRETE link to the caller's case can be named,
// and the name travels with it as the Relevance: line. No link, no promotion,
// whatever its grade or confidence — a HIGH-confidence sample about something
// else is still about something else.

// Relevance* are the links that promote a candidate into normal output. Each
// is a fact about the request and the candidate together, never about the
// machine they would run on.
const (
	// RelevanceExactFailure is the caller's own sanitized failure
	// fingerprint having been recorded against this sample. It is a direct
	// evidence link between the failure in hand and this candidate and
	// outranks every other consideration, including ecosystem.
	RelevanceExactFailure = "exact-failure-fingerprint"
	// RelevanceSamePackage is the caller and the sample naming one package.
	// A version gap does not cancel it: that is what an adaptation candidate
	// is, and the delta already states the gap.
	RelevanceSamePackage = "same-package"
	// RelevanceSameSymbol is a complete declared identity in common.
	RelevanceSameSymbol = "same-symbol"
	// RelevanceAdjacentSymbol is a neighbouring API on the same owner:
	// decimal.Decimal.Round beside decimal.Decimal.RoundBank. The adaptation
	// path between them is explainable, which is what makes it a candidate.
	RelevanceAdjacentSymbol = "adjacent-symbol"
	// RelevanceSameTool is the sample being about the very tool the failing
	// command ran. A pkg:generic CLI subject has no dependency tree to
	// overlap with, so the command's own name is the link.
	RelevanceSameTool = "same-tool"
	// RelevanceSharedDiagnostic is one structured error identifier named by
	// both the request and the sample.
	RelevanceSharedDiagnostic = "shared-diagnostic"
	// RelevanceNamedSubject is the caller's own question naming this
	// sample's package or symbol.
	RelevanceNamedSubject = "query-names-subject"
	// RelevanceGoalSemantics is the question and the sample's goal
	// describing the same operation in different words. It is the signal a
	// caller earns by saying what they want rather than which library does
	// it, and it is the one that has to be measured rather than asserted —
	// see goalSemanticsFloor.
	RelevanceGoalSemantics = "same-goal-semantics"
)

// goalSemanticsFloor is how much shared topic vocabulary makes two sentences
// the same subject.
//
// It is drawn from the two cases that have to land on opposite sides of it.
// "GitHub Actions workflow_dispatch deploys an immutable canonical main
// commit, serializes production deploys, checks out exact SHA" shares exactly
// one word with the goal of every sample it was answered with — "exact",
// "checks", "sha" — one apiece, and one word is a coincidence in a corpus
// this size. "Perform exact banker rounding of currency values with half to
// even at standard cash intervals" shares ten with the goal of the sample
// that proves precisely that, without naming shopspring or decimal once.
//
// Three distinct topic words, covering at least a third of the shorter side,
// is where those separate. The coverage clause is what stops three
// coincidences inside a long error paste from adding up to a subject.
const (
	goalSemanticsFloor    = 3
	goalSemanticsCoverage = 3 // shared × 3 ≥ min(query, goal) tokens
)

// Suppressed* are the stable reason codes a demoted candidate carries. They
// are read by machines — the hook trace, and the diagnostic payload — and the
// sentences beside them are for people and may be reworded.
const (
	// SuppressedUnrelatedEcosystem is the older, narrower gate: the failing
	// command does not build for the ecosystem the sample is pinned to.
	SuppressedUnrelatedEcosystem = "unrelated-ecosystem"
	// SuppressedInsufficientGoalOverlap is this gate: the candidate shares
	// no package, symbol, diagnostic or subject with the request, and what
	// it does share is environment coordinates.
	SuppressedInsufficientGoalOverlap = "insufficient-goal-overlap"
)

// diagnosticToken matches an error identifier confidently enough to treat a
// shared one as evidence: TS2352, E0277, ERR_MODULE_NOT_FOUND, CS1002.
//
// The digit-or-underscore requirement is load-bearing. Accepting every
// all-caps word made "checks out the exact SHA" share a diagnostic with any
// sample whose goal mentions SHA-256, which is the same false link this whole
// gate exists to close, re-entered through the error channel.
var diagnosticToken = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:[0-9_][A-Z0-9_]*)+$`)

var standardErrnoToken = map[string]struct{}{
	"EACCES": {}, "EAGAIN": {}, "EBADF": {}, "EBUSY": {}, "ECANCELED": {},
	"ECHILD": {}, "ECONNABORTED": {}, "ECONNREFUSED": {}, "ECONNRESET": {},
	"EEXIST": {}, "EFAULT": {}, "EINPROGRESS": {}, "EINTR": {}, "EINVAL": {},
	"EIO": {}, "EISDIR": {}, "ELOOP": {}, "EMFILE": {}, "ENAMETOOLONG": {},
	"ENETDOWN": {}, "ENETRESET": {}, "ENETUNREACH": {}, "ENFILE": {},
	"ENOBUFS": {}, "ENODEV": {}, "ENOENT": {}, "ENOEXEC": {}, "ENOMEM": {},
	"ENOSPC": {}, "ENOSYS": {}, "ENOTCONN": {}, "ENOTDIR": {}, "ENOTEMPTY": {},
	"ENOTSOCK": {}, "ENOTSUP": {}, "ENOTTY": {}, "ENXIO": {}, "EOPNOTSUPP": {},
	"EOVERFLOW": {}, "EPERM": {}, "EPIPE": {}, "EPROTO": {},
	"EPROTONOSUPPORT": {}, "EPROTOTYPE": {}, "ERANGE": {}, "EROFS": {},
	"ESRCH": {}, "ETIMEDOUT": {}, "EWOULDBLOCK": {}, "EXDEV": {},
}

func isDiagnosticToken(token string) bool {
	if diagnosticToken.MatchString(token) {
		return true
	}
	_, ok := standardErrnoToken[token]
	return ok
}

// RelevanceSignals names every concrete link between this request and this
// candidate, in a stable order. An empty result means the only thing the two
// have in common is where they run.
func (r SearchResult) RelevanceSignals(req SearchRequest, argv []string) []string {
	var out []string
	add := func(signal string) {
		for _, have := range out {
			if have == signal {
				return
			}
		}
		out = append(out, signal)
	}

	if r.ExactFailureMatched {
		add(RelevanceExactFailure)
	}
	if r.Case == nil {
		return out
	}
	if sharesPackage(req.Packages, r.Case.Packages) {
		add(RelevanceSamePackage)
	}

	declared := r.declaredSymbols()
	requested := append(append([]string{}, req.Symbols...), req.ContextSymbols...)
	if len(searchrelevance.MatchedDeclaredSymbols(requested, declared)) > 0 {
		add(RelevanceSameSymbol)
	} else if sharesSymbolOwner(requested, declared) {
		add(RelevanceAdjacentSymbol)
	}

	if namesTool(r.packageNames(), argv) {
		add(RelevanceSameTool)
	}

	text := r.subjectText()
	for _, code := range requestDiagnostics(req) {
		if searchrelevance.ContainsIdentifier(text, code) {
			add(RelevanceSharedDiagnostic)
			break
		}
	}

	strong, _ := searchrelevance.Signal(req.Query, r.Case.Goal, r.packageNames(), declared)
	if strong > 0 {
		add(RelevanceNamedSubject)
	}
	if sameGoalSemantics(req.Query, r.Case.Goal) {
		add(RelevanceGoalSemantics)
	}
	return out
}

// sameGoalSemantics reports whether the question and the goal sentence
// describe the same operation. See goalSemanticsFloor for where the bar came
// from.
func sameGoalSemantics(query, goal string) bool {
	shared, queryTokens, goalTokens := searchrelevance.GoalOverlap(query, goal)
	if shared < goalSemanticsFloor {
		return false
	}
	shorter := queryTokens
	if goalTokens < shorter {
		shorter = goalTokens
	}
	return shared*goalSemanticsCoverage >= shorter
}

// RelevantToRequest reports whether this candidate may be rendered as normal
// output for this request.
//
// A candidate that declares no subject at all — no case, so no goal, no
// packages, no symbols — is not judged. The gate reads what a sample says it
// is ABOUT, and with nothing to read the honest answer is "no opinion", not
// "irrelevant": calling it irrelevant would be a verdict reached from the
// absence of evidence. Both producers attach the case, so this is the shape
// of a degenerate payload rather than of a weak answer.
func (r SearchResult) RelevantToRequest(req SearchRequest, argv []string) bool {
	if r.Case == nil {
		return true
	}
	return len(r.RelevanceSignals(req, argv)) > 0
}

// RelevanceLine is the one-line, mechanically generated justification that
// travels with a candidate that passed the gate. A reader who cannot see why
// a sample was shown has no way to judge it, and "it scored 0.67" is not a
// reason — it is the absence of one.
func (r SearchResult) RelevanceLine(req SearchRequest, argv []string) string {
	signals := r.RelevanceSignals(req, argv)
	if len(signals) == 0 {
		return ""
	}
	var parts []string
	for _, signal := range signals {
		switch signal {
		case RelevanceExactFailure:
			parts = append(parts, "your sanitized failure fingerprint was recorded against this sample")
		case RelevanceSamePackage:
			parts = append(parts, "it is about a package you named")
		case RelevanceSameSymbol:
			parts = append(parts, "it declares a symbol you asked about")
		case RelevanceAdjacentSymbol:
			parts = append(parts, "it declares a neighbouring API on a symbol you asked about")
		case RelevanceSameTool:
			parts = append(parts, "it is about the tool this command ran")
		case RelevanceSharedDiagnostic:
			parts = append(parts, "it names the same structured error")
		case RelevanceNamedSubject:
			parts = append(parts, "your question names its package or symbol")
		case RelevanceGoalSemantics:
			parts = append(parts, "its goal describes the operation you asked about")
		}
	}
	return "Relevance: " + strings.Join(parts, "; ") + "."
}

// SuppressionReason is the stable code for why this candidate did not reach
// normal output, or "" when it did.
//
// argv is empty on the paths that answered a QUESTION rather than a command,
// and there the ecosystem gate must not run at all: it reads an empty argv as
// "this command builds for no ecosystem I know" and rejects every pinned
// sample on that basis, which is a verdict about a command that never ran.
func (r SearchResult) SuppressionReason(req SearchRequest, argv []string) string {
	if len(argv) > 0 && r.UnrelatedToCommand(argv) {
		return SuppressedUnrelatedEcosystem
	}
	if !r.RelevantToRequest(req, argv) {
		return SuppressedInsufficientGoalOverlap
	}
	return ""
}

// SuppressedCandidate is a retrieval candidate that did not reach normal
// output, kept so the decision is observable rather than invisible.
//
// Retrieval and rendering are different questions and this type is the seam
// between them: the engine found something, and the output layer declined to
// present it as an answer. It carries the coordinate and the reason, never
// the sample body — a suppressed candidate that ships its contract has not
// been suppressed.
type SuppressedCandidate struct {
	SampleID   string     `json:"sampleId,omitempty"`
	Packages   []string   `json:"packages,omitempty"`
	Grade      MatchGrade `json:"match,omitempty"`
	Confidence string     `json:"confidence,omitempty"`
	Score      float64    `json:"score,omitempty"`
	Reason     string     `json:"suppressedReason"`
	Signals    []string   `json:"relevanceSignals,omitempty"`
}

// GateNormalOutput splits a retrieval response into what may be rendered as
// normal output and what may not.
//
// When nothing survives, the response becomes a MISS. That is the honest
// answer and not a demotion: NO_SAFE_MATCH means this network has no sample
// it built for the caller's case, which is exactly what "everything it
// retrieved is about something else" amounts to. The alternative — rendering
// the nearest neighbour anyway under a REFERENCE_CANDIDATE label — is the
// forced HIT that goal.md §3.8 exists to forbid.
//
// argv is the failing command's own words on the paths that have one, and
// empty on the paths that answered a question.
func GateNormalOutput(req SearchRequest, resp SearchResponse, argv []string) (SearchResponse, []SuppressedCandidate) {
	if resp.Miss || len(resp.Results) == 0 {
		return resp, nil
	}
	var (
		kept       []SearchResult
		suppressed []SuppressedCandidate
	)
	for _, r := range resp.Results {
		reason := r.SuppressionReason(req, argv)
		if reason == "" {
			kept = append(kept, r)
			continue
		}
		c := SuppressedCandidate{
			SampleID: r.SampleID, Grade: r.Grade, Confidence: r.Confidence,
			Score: r.Score, Reason: reason, Signals: r.RelevanceSignals(req, argv),
		}
		if r.Case != nil {
			c.Packages = r.Case.Packages
		}
		suppressed = append(suppressed, c)
	}
	resp.Results = kept
	if len(kept) == 0 {
		resp.Miss = true
	}
	return resp, suppressed
}

func (r SearchResult) declaredSymbols() []string {
	if r.Case == nil {
		return nil
	}
	return r.Case.Symbols
}

func (r SearchResult) packageNames() []string {
	if r.Case == nil {
		return nil
	}
	var out []string
	for _, raw := range r.Case.Packages {
		if p, err := ParsePURL(raw); err == nil {
			out = append(out, p.Name)
		} else {
			out = append(out, raw)
		}
	}
	return out
}

// subjectText is everything the sample says it is ABOUT. The contract lines
// are included because that is where an error identifier is actually named —
// a goal sentence says what was attempted, and the assertion underneath it
// says what was raised.
func (r SearchResult) subjectText() string {
	if r.Case == nil {
		return ""
	}
	parts := []string{r.Case.Goal, r.Case.Believed}
	parts = append(parts, r.Case.Symbols...)
	parts = append(parts, r.Case.Contract...)
	parts = append(parts, r.packageNames()...)
	return strings.Join(parts, "\n")
}

// requestDiagnostics is every error identifier the request carries. A code
// the caller declared is taken at its word; one lifted out of free text has
// to look like a code, because most words do not.
func requestDiagnostics(req SearchRequest) []string {
	var out []string
	if code := strings.TrimSpace(req.ErrorCode); code != "" {
		out = append(out, code)
	}
	for _, field := range strings.FieldsFunc(req.Query, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	}) {
		if isDiagnosticToken(field) {
			out = append(out, field)
		}
	}
	return out
}

// commandToolAliases are the names one tool is published under. The list is
// deliberately short: it exists so a pwsh failure can find a sample about
// powershell, not so every command can claim kinship with every package.
var commandToolAliases = map[string][]string{
	"pwsh":       {"powershell"},
	"powershell": {"pwsh"},
	"python3":    {"python"},
	"pip3":       {"pip"},
	"tsc":        {"typescript"},
	"node":       {"nodejs"},
	"gradlew":    {"gradle"},
	"mvnw":       {"mvn", "maven"},
}

// namesTool reports whether one of the sample's packages IS the tool the
// failing command ran. The last path segment is compared too, because a
// generic CLI subject is published as a bare name while a module is
// published as an import path.
func namesTool(packageNames, argv []string) bool {
	tool := CommandTool(argv)
	if tool == "" {
		return false
	}
	want := map[string]bool{tool: true}
	for _, alias := range commandToolAliases[tool] {
		want[alias] = true
	}
	for _, name := range packageNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if want[name] {
			return true
		}
		if i := strings.LastIndexAny(name, "/"); i >= 0 && want[name[i+1:]] {
			return true
		}
	}
	return false
}

func sharesPackage(requested, declared []string) bool {
	if len(requested) == 0 || len(declared) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, raw := range declared {
		if p, err := ParsePURL(raw); err == nil {
			have[strings.ToLower(p.Ecosystem+"/"+p.Name)] = true
		}
	}
	for _, raw := range requested {
		if p, err := ParsePURL(raw); err == nil &&
			have[strings.ToLower(p.Ecosystem+"/"+p.Name)] {
			return true
		}
	}
	return false
}

// sharesSymbolOwner reports whether a requested and a declared symbol hang
// off the same owner — the identifier up to the last separator. It is what
// makes "same package, nearby symbol" a candidate rather than a miss, and it
// requires the owner to identify something: an owner of "json" or "client"
// would connect half the corpus to itself.
func sharesSymbolOwner(requested, declared []string) bool {
	owners := map[string]bool{}
	for _, symbol := range declared {
		if owner := symbolOwner(symbol); owner != "" {
			owners[owner] = true
		}
	}
	for _, symbol := range requested {
		if owner := symbolOwner(symbol); owner != "" && owners[owner] {
			return true
		}
	}
	return false
}

func symbolOwner(symbol string) string {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	i := strings.LastIndexAny(symbol, "./:#")
	if i <= 0 {
		return ""
	}
	owner := symbol[:i]
	for _, token := range searchrelevance.ContentTokens(owner) {
		if !searchrelevance.IsGeneric(token) {
			return owner
		}
	}
	return ""
}
