package web

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// The Failure Issue: every failure this network recorded under ONE normalized
// identity, read across the releases and environments it reproduced in.
//
// A failure CLUSTER is a fact about one release in one environment, and the
// package page lists them that way on purpose. It is the wrong grain for the
// question a reader actually arrives with, which is where a failure starts,
// where it stops, and what differs across that line. pgx/v5 carries 133
// clusters; nobody reads 133 rows to work out that eleven of them are one
// cause and the rest are another.
//
// The identity is the normalized fingerprint and never the exit status. Exit 1
// is what almost every broken build returns, so a stage/exit-code bucket puts a
// missing module and a failing assertion in the same drawer and then invites a
// reader to reason about the drawer. The fingerprint already carries the stage,
// the failing toolchain, the error code and the normalized error text, so two
// causes that share an exit code stay two issues without this file having to
// decide anything.
//
// Everything here is a pure function over material the page has already read:
// the cluster documents, the dependency edges and the per-release stage counts.
// Nothing new is stored, and no verdict is invented — a release nothing
// measured stays unmeasured all the way to the screen.

// failureIssueVerdict is what this network can say about ONE issue at ONE
// release, at the stage that issue belongs to.
//
// issueVerdictPass is deliberately the weaker claim it looks like: the release
// has a passing observation at this stage and no record of this issue. It is
// not a proof of absence, and the boundary it bounds is described as the
// nearest KNOWN one for exactly that reason.
type failureIssueVerdict string

const (
	issueVerdictUnmeasured failureIssueVerdict = ""
	issueVerdictPass       failureIssueVerdict = "PASS"
	issueVerdictFail       failureIssueVerdict = "FAIL"
)

// Which way the verdict changes across a boundary.
const (
	boundaryStarts = "starts"
	boundaryStops  = "stops"
)

// What this network can say about a dependency that differs across a
// boundary. The distinction is the whole point of listing them at all.
const (
	// causalEvidence: the same receipt that recorded the failure also
	// recorded this child at this version. That is a measurement.
	causalEvidence = "evidence"
	// causalHypothesis: the child's version moved across the boundary and
	// nothing ties the failure to it. That is a correlation.
	causalHypothesis = "hypothesis"
)

// failureIssueEnv is one environment an issue reproduced in, with what it
// cost there.
type failureIssueEnv struct {
	Summary   string
	Count     int64
	FirstSeen string
	LastSeen  string
}

// failureIssue is the aggregate. Its fields are the signature a reader has to
// be able to recognise again, plus where it was seen.
type failureIssue struct {
	// ID addresses the issue inside its package. It is derived from the
	// identity below, so a link survives re-materialization of the snapshot
	// that produced the clusters.
	ID   string
	Href string

	// Signature.
	Fingerprint      string // full identity, "" when none was established
	FingerprintShort string
	Stage            string
	Termination      string
	ErrorCode        string
	ActualToolchain  string
	StageEvidence    string
	OuterCommands    string
	ErrorSummary     string
	ErrorSummaryFull string

	// What was preserved of it.
	EvidenceQuality string
	EvidenceGap     bool
	EvidenceGapKind string

	// Where it was seen.
	Count               int64
	Versions            []string // newest first
	Symbols             []string
	Environments        []failureIssueEnv
	FirstSeen           string
	LastSeen            string
	Hypotheses          []hypothesisView
	RegressionCandidate bool
}

// failureBoundary is the nearest pair of DECIDED releases the verdict changes
// across. UnmeasuredBetween is how many releases sit between them that nothing
// can speak for: without it "1.9.0 → 1.12.0" reads as though the two releases
// were next to each other.
type failureBoundary struct {
	Kind          string // starts | stops
	LowerVersion  string
	HigherVersion string
	LowerVerdict  string
	HigherVerdict string
	LowerHref     string
	HigherHref    string
	// PassVersion and FailVersion are the same two releases named by what
	// they measured rather than by their order, because that is the pair a
	// dependency comparison needs.
	PassVersion       string
	FailVersion       string
	UnmeasuredBetween int
	Changed           []failureCausalEdge
	// TreeUnread says one side of this boundary has no resolved tree at all,
	// so the comparison could not be made. An empty Changed list must not
	// read as "nothing moved".
	TreeUnread bool
}

// failureCausalEdge is one dependency that differs across a boundary, and
// what this network is entitled to say about it.
type failureCausalEdge struct {
	Library     string
	PassVersion string
	FailVersion string
	Href        string
	Basis       string // causalEvidence | causalHypothesis
}

// buildFailureIssues folds a package's failure clusters into issues.
//
// The count rule is the one buildClusters established for the same reason:
// the recorder files one observation against the package AND one against every
// symbol it detected, so the package-level count already contains the
// symbol's. Within one (environment, release) bucket the largest is kept and
// never the sum; the buckets themselves add up, because a failure reproducing
// somewhere else is a separate event.
func buildFailureIssues(clusters []failureCluster) []failureIssue {
	type bucket struct{ env, versions string }
	type agg struct {
		issue   failureIssue
		key     string
		buckets map[bucket]int64
		envSeen map[string][2]string // env → first, last
		envOrd  []string
		symbols map[string]bool
	}
	byKey := map[string]*agg{}
	var order []string

	for _, c := range clusters {
		unfingerprinted := clusterUnfingerprinted(c)
		key := failureIssueKey(c, unfingerprinted)
		a := byKey[key]
		if a == nil {
			a = &agg{
				key:     key,
				buckets: map[bucket]int64{},
				envSeen: map[string][2]string{},
				symbols: map[string]bool{},
			}
			a.issue.ID = failureIssueID(key)
			a.issue.EvidenceGap = unfingerprinted
			if !unfingerprinted {
				a.issue.Fingerprint = c.Fingerprint
				a.issue.FingerprintShort = shortHash(c.Fingerprint)
			}
			byKey[key] = a
			order = append(order, key)
		}
		issue := &a.issue
		if issue.Stage == "" {
			issue.Stage = c.Stage
		}
		if issue.Termination == "" {
			issue.Termination = terminationLabel(c)
		}
		if issue.ErrorCode == "" {
			issue.ErrorCode = c.ErrorCode
		}
		if issue.ActualToolchain == "" {
			issue.ActualToolchain = c.ActualToolchain
		}
		if issue.StageEvidence == "" {
			issue.StageEvidence = c.StageEvidence
		}
		if issue.OuterCommands == "" {
			issue.OuterCommands = failureOuterCommands(c)
		}
		if issue.ErrorSummary == "" && c.ErrorSummary != "" {
			issue.ErrorSummary, issue.ErrorSummaryFull = clusterErrorSummary(c.ErrorSummary)
		}
		if issue.EvidenceQuality == "" {
			issue.EvidenceQuality = c.EvidenceQuality
		}
		if issue.EvidenceGapKind == "" {
			issue.EvidenceGapKind = c.EvidenceGapKind
		}
		if c.EvidenceGapKind != "" {
			issue.EvidenceGap = true
		}
		if c.RegressionCandidate {
			issue.RegressionCandidate = true
		}
		if len(issue.Hypotheses) == 0 {
			issue.Hypotheses = namedHypotheses(c.Hypotheses)
		}
		if c.Symbol != "" {
			a.symbols[c.Symbol] = true
		}
		issue.Versions = appendMissing(issue.Versions, c.Versions...)

		env := joinEnvSummary(c.EnvSummary)
		b := bucket{env: env, versions: strings.Join(c.Versions, ",")}
		if n := clusterCount(c); n > a.buckets[b] {
			a.buckets[b] = n
		}
		seen, had := a.envSeen[env]
		if !had {
			a.envOrd = append(a.envOrd, env)
		}
		a.envSeen[env] = [2]string{earliest(seen[0], c.FirstSeen), latest(seen[1], c.LastSeen)}
		issue.FirstSeen = earliest(issue.FirstSeen, c.FirstSeen)
		issue.LastSeen = latest(issue.LastSeen, c.LastSeen)
	}

	out := make([]failureIssue, 0, len(byKey))
	for _, key := range order {
		a := byKey[key]
		issue := a.issue
		perEnv := map[string]int64{}
		for b, n := range a.buckets {
			issue.Count += n
			perEnv[b.env] += n
		}
		for _, env := range a.envOrd {
			seen := a.envSeen[env]
			issue.Environments = append(issue.Environments, failureIssueEnv{
				Summary: env, Count: perEnv[env],
				FirstSeen: datePart(seen[0]), LastSeen: datePart(seen[1]),
			})
		}
		sort.Slice(issue.Environments, func(i, j int) bool {
			if issue.Environments[i].Count != issue.Environments[j].Count {
				return issue.Environments[i].Count > issue.Environments[j].Count
			}
			return issue.Environments[i].Summary < issue.Environments[j].Summary
		})
		for sym := range a.symbols {
			issue.Symbols = append(issue.Symbols, sym)
		}
		sort.Strings(issue.Symbols)
		issue.Versions = sortedVersionsDesc(issue.Versions)
		issue.FirstSeen, issue.LastSeen = datePart(issue.FirstSeen), datePart(issue.LastSeen)
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// clusterUnfingerprinted reports that a cluster has no established cause.
//
// A historical hash and an empty-error hash are provenance, not failure
// identities: the producer says so by writing the evidence quality beside
// them, and both the cluster list and the issue aggregate have to read it the
// same way or a reader following a fingerprint from one lands on the other.
func clusterUnfingerprinted(c failureCluster) bool {
	return c.Fingerprint == "" ||
		c.EvidenceQuality == string(domain.EvidenceMissing) ||
		c.EvidenceQuality == string(domain.EvidenceLegacyIncomplete)
}

// failureIssueKey is the identity two clusters must share to be one issue.
//
// A fingerprinted cluster is keyed by the fingerprint alone: it already
// encodes the stage, the failing toolchain, the error code and the normalized
// error, which is precisely the material that separates two causes sharing an
// exit status.
//
// A cluster whose evidence was never preserved has NO established cause. Its
// stored hash is provenance rather than an identity, so it is keyed by the
// structured state that did survive and can never merge into a proven issue —
// attaching real failures to a cause nothing proved is the failure mode this
// separation exists to prevent.
func failureIssueKey(c failureCluster, unfingerprinted bool) string {
	if !unfingerprinted {
		return "fp|" + c.Fingerprint
	}
	term := domain.FailureTermination{
		Kind: domain.TerminationKind(c.TerminationKind), ExitCode: c.ExitCode,
		Signal: c.Signal, TimeoutMillis: c.TimeoutMillis,
	}
	return strings.Join([]string{"gap", c.Stage, strings.ToLower(c.ActualToolchain),
		term.FingerprintCoordinate(), c.ErrorCode, c.EvidenceGapKind}, "|")
}

// failureIssueID is the short, URL-safe address of an identity. It is a
// function of the identity and of nothing else, so the link a reader shares
// keeps resolving after the snapshot behind it is rebuilt.
func failureIssueID(key string) string {
	return strings.TrimPrefix(domain.SHA256Hex([]byte(key)), "sha256:")[:16]
}

func failureIssueByID(issues []failureIssue, id string) (failureIssue, bool) {
	for _, issue := range issues {
		if issue.ID == id {
			return issue, true
		}
	}
	return failureIssue{}, false
}

// failureIssueVerdicts decides, per release, what this network can say about
// ONE issue at the stage that issue belongs to.
//
// stagePass is how many passing observations each release recorded at that
// stage. A release with none is unmeasured, not passing: the difference is the
// whole reason the boundary below can be called a known one.
func failureIssueVerdicts(issue failureIssue, versions []string, stagePass map[string]int64) map[string]failureIssueVerdict {
	failed := map[string]bool{}
	for _, v := range issue.Versions {
		failed[v] = true
	}
	out := make(map[string]failureIssueVerdict, len(versions))
	for _, v := range versions {
		switch {
		case failed[v]:
			out[v] = issueVerdictFail
		case stagePass[v] > 0:
			out[v] = issueVerdictPass
		default:
			out[v] = issueVerdictUnmeasured
		}
	}
	return out
}

// failureIssueBoundaries returns every place the verdict changes between two
// adjacent DECIDED releases, oldest boundary first.
//
// Releases nothing measured do not close a gap and do not open one. They are
// skipped so the pair named is the nearest KNOWN one, and counted so the page
// can say how much of the distance between them is unread.
func failureIssueBoundaries(versions []string, verdicts map[string]failureIssueVerdict) []failureBoundary {
	ordered := sortedVersionsAsc(versions)
	decided := make([]int, 0, len(ordered))
	for i, v := range ordered {
		if verdicts[v] != issueVerdictUnmeasured {
			decided = append(decided, i)
		}
	}
	var out []failureBoundary
	for i := 1; i < len(decided); i++ {
		lo, hi := ordered[decided[i-1]], ordered[decided[i]]
		loV, hiV := verdicts[lo], verdicts[hi]
		if loV == hiV {
			continue
		}
		b := failureBoundary{
			LowerVersion: lo, HigherVersion: hi,
			LowerVerdict: string(loV), HigherVerdict: string(hiV),
			UnmeasuredBetween: decided[i] - decided[i-1] - 1,
		}
		if loV == issueVerdictPass {
			b.Kind, b.PassVersion, b.FailVersion = boundaryStarts, lo, hi
		} else {
			b.Kind, b.PassVersion, b.FailVersion = boundaryStops, hi, lo
		}
		out = append(out, b)
	}
	return out
}

// failureIssueCausalEdges lists the dependencies that differ between the two
// sides of a boundary, and says which side of the evidence line each one is on.
//
// A version having moved is a correlation and is labelled hypothesis. The one
// thing here that is a measurement is a receipt that recorded BOTH the failure
// and the tree it resolved, which is the same-receipt rule #178 established for
// the dependency-health column; nothing else may be spelled as evidence.
//
// A side with no resolved tree returns nothing rather than a list of
// removals: an unread tree and an empty tree are opposite facts, and the
// caller renders the difference.
func failureIssueCausalEdges(eco string, edges []DependencyEdge, passVersion, failVersion string) []failureCausalEdge {
	pass := resolvedChildren(edges, passVersion)
	fail := resolvedChildren(edges, failVersion)
	if len(pass) == 0 || len(fail) == 0 {
		return nil
	}
	proven := map[string]bool{}
	for _, e := range edges {
		if e.ParentVersion == failVersion && e.SameReceipt && e.Outcome == "fail" && e.ChildName != "" {
			proven[e.ChildName] = true
		}
	}
	names := map[string]bool{}
	for name := range pass {
		names[name] = true
	}
	for name := range fail {
		names[name] = true
	}
	out := make([]failureCausalEdge, 0, len(names))
	for name := range names {
		before, after := joinVersions(pass[name]), joinVersions(fail[name])
		if before == after {
			continue
		}
		edge := failureCausalEdge{
			Library: name, PassVersion: before, FailVersion: after,
			Basis: causalHypothesis,
		}
		if proven[name] {
			edge.Basis = causalEvidence
		}
		if after != "" {
			edge.Href = depHref(eco, name, fail[name][0])
		} else if before != "" {
			edge.Href = depHref(eco, name, pass[name][0])
		}
		out = append(out, edge)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Basis == causalEvidence) != (out[j].Basis == causalEvidence) {
			return out[i].Basis == causalEvidence
		}
		return out[i].Library < out[j].Library
	})
	return out
}

// resolvedChildren is what one release resolved each of its children to. A
// child resolved twice under one release keeps both versions: that collision
// is exactly the thing a comparison must not hide.
func resolvedChildren(edges []DependencyEdge, version string) map[string][]string {
	if version == "" {
		return nil
	}
	out := map[string][]string{}
	for _, e := range edges {
		if e.ParentVersion != version || e.ChildName == "" || e.ChildVersion == "" {
			continue
		}
		if !contains(out[e.ChildName], e.ChildVersion) {
			out[e.ChildName] = append(out[e.ChildName], e.ChildVersion)
		}
	}
	for name := range out {
		sort.Strings(out[name])
	}
	return out
}

// failureIssueVersionWindow bounds the releases a Failure Issue reads and
// compares. The releases that matter are the affected ones and their
// neighbours — a boundary can only be next to a failure — so the cap falls on
// everything further away.
//
// An affected release absent from the version list is added rather than
// dropped. A golang module is published as both "1.6.0" and "v1.6.0" and only
// one spelling carries a version row, so the release a failure was RECORDED
// on can be missing from the list — and taking it out of the window would
// take the FAIL off a page whose entire subject is that failure.
func failureIssueVersionWindow(versions, affected []string, span, max int) []string {
	ordered := sortedVersionsDesc(appendMissing(append([]string(nil), versions...), affected...))
	if len(ordered) == 0 || max <= 0 {
		return nil
	}
	at := map[string]int{}
	for i, v := range ordered {
		at[v] = i
	}
	var anchors []int
	for _, v := range affected {
		if i, ok := at[v]; ok {
			anchors = append(anchors, i)
		}
	}
	if len(anchors) == 0 {
		if len(ordered) > max {
			ordered = ordered[:max]
		}
		return ordered
	}
	type scored struct{ idx, dist int }
	var picked []scored
	for i := range ordered {
		best := -1
		for _, a := range anchors {
			d := i - a
			if d < 0 {
				d = -d
			}
			if best < 0 || d < best {
				best = d
			}
		}
		if best <= span {
			picked = append(picked, scored{i, best})
		}
	}
	sort.Slice(picked, func(i, j int) bool {
		if picked[i].dist != picked[j].dist {
			return picked[i].dist < picked[j].dist
		}
		return picked[i].idx < picked[j].idx
	})
	if len(picked) > max {
		picked = picked[:max]
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].idx < picked[j].idx })
	out := make([]string, 0, len(picked))
	for _, p := range picked {
		out = append(out, ordered[p.idx])
	}
	return out
}

// ---------------------------------------------------------------------------
// Small shared helpers.

// namedHypotheses drops UNKNOWN for the reason the cluster list drops it:
// "UNKNOWN 100%" under a note explaining that hypotheses are inference is
// noise dressed as analysis.
func namedHypotheses(hyps []domain.FailureHypothesis) []hypothesisView {
	var out []hypothesisView
	for _, h := range hyps {
		if h.Domain == domain.FailUnknown {
			continue
		}
		out = append(out, hypothesisView{Domain: string(h.Domain), Pct: i18n.FormatPercent("en", h.Confidence)})
	}
	return out
}

func clusterCount(c failureCluster) int64 {
	if c.Count != 0 {
		return c.Count
	}
	return c.ObservationCount
}

func appendMissing(dst []string, values ...string) []string {
	for _, v := range values {
		if v != "" && !contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func joinVersions(versions []string) string { return strings.Join(versions, ", ") }

// earliest and latest compare RFC3339 UTC strings, which order lexically. An
// empty value is "not recorded" and never wins a minimum.
func earliest(a, b string) string {
	if a == "" || (b != "" && b < a) {
		return b
	}
	return a
}

func latest(a, b string) string {
	if b > a {
		return b
	}
	return a
}

func sortedVersionsDesc(versions []string) []string {
	out := append([]string(nil), versions...)
	sort.Slice(out, func(i, j int) bool {
		if c := domain.CompareVersions(out[i], out[j]); c != 0 {
			return c > 0
		}
		return out[i] > out[j]
	})
	return out
}

func sortedVersionsAsc(versions []string) []string {
	out := sortedVersionsDesc(versions)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
