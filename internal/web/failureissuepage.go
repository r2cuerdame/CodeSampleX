package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

const (
	// failureIssueVersionSpan is how far either side of an affected release
	// the page looks for a boundary. A boundary can only sit next to a
	// failure, so reading the whole release history buys nothing and costs a
	// snapshot read per release.
	failureIssueVersionSpan = 3
	// failureIssueMaxVersions bounds the snapshot reads one issue makes.
	failureIssueMaxVersions = 9
	// How many representative published answers the page offers, and over how
	// many affected releases. The list is a way in, not an inventory.
	failureIssueSampleReleases    = 3
	failureIssueSamplesPerRelease = 3
	failureIssueMaxSamples        = 6
)

// failureIssueIDRe guards the address. The id is a fixed-width hex digest, so
// anything else is a URL nobody could have been given.
var failureIssueIDRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

func failureIssueHref(eco, name, id string) string {
	if id == "" {
		return ""
	}
	return pkgHref(eco, name) + "?issue=" + url.QueryEscape(id)
}

// failureIssueRelease is one release of the package as this issue sees it.
type failureIssueRelease struct {
	Version string
	Href    string
	// Verdict is the machine word — PASS, FAIL, or empty for a release
	// nothing measured at this stage. Key is its lowercase spelling, which
	// the row carries as a data attribute so the state is readable without
	// parsing a translated label. Label is what the reader sees.
	Verdict string
	Key     string // pass | fail | unmeasured
	Tone    string // chip modifier: high | elevated | unknown
	Label   string
	// PassObservations is why a PASS says PASS. A verdict with a number
	// behind it can be checked; one without is an assertion.
	PassObservations int64
}

type failureIssuePageData struct {
	basePage
	Ecosystem   string
	Name        string
	Crumbs      []crumb
	Issue       failureIssue
	Releases    []failureIssueRelease
	Boundaries  []failureBoundary
	DepsMatrix  *dependencyMatrix
	Gaps        []string
	Samples     []SampleListItem
	PackageHref string
}

// failureIssuePage answers the three questions a reader arrives with when a
// build breaks: where does this failure start, where does it stop, and what
// differs across that line.
//
// It reads nothing new. The clusters, the per-release stage counts and the
// dependency edges are the same materialized documents the package page
// already consumes; what this page adds is the grain — one normalized
// identity, read across the releases and environments it reproduced in,
// instead of a list of per-release-per-environment rows a reader has to fold
// together by eye.
func (s *site) failureIssuePage(w http.ResponseWriter, r *http.Request, lang, eco, name, id string) {
	if !failureIssueIDRe.MatchString(id) {
		s.notFound(w, r, lang)
		return
	}
	raw, _, err := s.d.Store.FailureClusters(r.Context(), eco, name)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	issue, ok := failureIssueByID(buildFailureIssues(decodeFailureClusters(raw)), id)
	if !ok {
		s.notFound(w, r, lang)
		return
	}
	issue.Href = failureIssueHref(eco, name, issue.ID)

	versions, err := s.d.Store.PackageVersions(r.Context(), eco, name)
	if err != nil {
		versions = nil
	}
	// A cluster recorded against no release cannot be placed on the release
	// axis. Drawing the axis anyway would show every release as PASS or
	// unmeasured with the failure nowhere on it — the opposite of what is
	// known — so the axis is withheld and the gap section says why.
	var window []string
	if len(issue.Versions) > 0 {
		window = failureIssueVersionWindow(versions, issue.Versions,
			failureIssueVersionSpan, failureIssueMaxVersions)
	}
	stagePass := s.stagePassByRelease(r.Context(), eco, name, issue.Stage, window)
	verdicts := failureIssueVerdicts(issue, window, stagePass)

	b := s.page(r, lang, i18n.T(lang, "issue.title", name, eco)+" — CodeSampleX",
		failureIssueDescription(lang, name, issue))
	// The trail is the package's. The issue is one thing ABOUT that package
	// and not a level of the coordinate, so it does not extend the ladder.
	crumbs := leaf(append(recordCrumbs(b, eco, name, "", ""),
		crumb{Label: i18n.T(lang, "issue.crumb")}))

	releases := make([]failureIssueRelease, 0, len(window))
	for _, v := range window {
		row := failureIssueRelease{
			Version: v,
			Href:    b.WithLang(versionHref(eco, name, v)),
			Verdict: string(verdicts[v]),
			Key:     verdictKey(verdicts[v]),
			Tone:    verdictTone(verdicts[v]),
			Label:   verdictLabel(lang, verdicts[v]),
		}
		if verdicts[v] == issueVerdictPass {
			row.PassObservations = stagePass[v]
		}
		releases = append(releases, row)
	}

	boundaries := failureIssueBoundaries(window, verdicts)
	var edges []DependencyEdge
	if rows, err := s.d.Store.Dependencies(r.Context(), eco, name); err == nil {
		edges = rows
	}
	for i := range boundaries {
		bd := &boundaries[i]
		bd.LowerHref = b.WithLang(versionHref(eco, name, bd.LowerVersion))
		bd.HigherHref = b.WithLang(versionHref(eco, name, bd.HigherVersion))
		bd.Changed = failureIssueCausalEdges(eco, edges, bd.PassVersion, bd.FailVersion)
		bd.TreeUnread = len(resolvedChildren(edges, bd.PassVersion)) == 0 ||
			len(resolvedChildren(edges, bd.FailVersion)) == 0
		for j := range bd.Changed {
			bd.Changed[j].Href = b.WithLang(bd.Changed[j].Href)
		}
	}

	s.render(w, "failureissue", http.StatusOK, failureIssuePageData{
		basePage: b, Ecosystem: eco, Name: name, Crumbs: crumbs,
		Issue:      issue,
		Releases:   releases,
		Boundaries: boundaries,
		// The matrix is built from the edges of the releases in the window —
		// the dependency versions AROUND the PASS/FAIL observations, which is
		// the comparison the boundary above points at.
		DepsMatrix:  buildDependencyMatrix(eco, edgesForVersions(edges, window)),
		Gaps:        failureIssueGaps(lang, issue, verdicts, boundaries),
		Samples:     s.failureIssueSamples(r.Context(), eco, name, issue),
		PackageHref: b.WithLang(pkgHref(eco, name)),
	})
}

// stagePassByRelease counts the passing observations each release recorded at
// ONE stage.
//
// It reads the package-level snapshot only. A per-symbol snapshot describes
// one API of the release, and a boundary claim assembled out of whichever
// symbols happened to be materialized would move as the corpus grows; a
// release with no package-level snapshot stays unmeasured instead, which is
// the honest answer and the one the verdict is built to carry.
func (s *site) stagePassByRelease(ctx context.Context, eco, name, stage string, versions []string) map[string]int64 {
	out := make(map[string]int64, len(versions))
	if stage == "" {
		return out
	}
	for _, v := range versions {
		purl := domain.PURL{Ecosystem: eco, Name: name, Version: v}.String()
		raw, ok := s.d.Store.SnapshotJSON(ctx, purl, "")
		if !ok {
			continue
		}
		var doc snapshotDoc
		if json.Unmarshal([]byte(raw), &doc) != nil {
			continue
		}
		for _, row := range doc.Rows {
			out[v] += row.ByStage[stage].Pass
		}
	}
	return out
}

// failureIssueSamples offers a way into the published answers written against
// the affected releases. It is bounded on both axes: this is an entry point,
// not an inventory, and the release pages remain the unabridged list.
func (s *site) failureIssueSamples(ctx context.Context, eco, name string, issue failureIssue) []SampleListItem {
	var out []SampleListItem
	for i, v := range issue.Versions {
		if i >= failureIssueSampleReleases || len(out) >= failureIssueMaxSamples {
			break
		}
		items, err := s.d.Store.ReleaseSamples(ctx, eco, name, v, failureIssueSamplesPerRelease)
		if err != nil {
			continue
		}
		for _, item := range items {
			if len(out) >= failureIssueMaxSamples {
				break
			}
			out = append(out, item)
		}
	}
	return out
}

// edgesForVersions narrows the dependency edges to the releases in the
// window, so the matrix compares the trees around the boundary rather than
// every release the package has ever had.
func edgesForVersions(edges []DependencyEdge, versions []string) []DependencyEdge {
	keep := make(map[string]bool, len(versions))
	for _, v := range versions {
		keep[v] = true
	}
	out := make([]DependencyEdge, 0, len(edges))
	for _, e := range edges {
		if keep[e.ParentVersion] {
			out = append(out, e)
		}
	}
	return out
}

// failureIssueGaps states what this page could NOT establish.
//
// It is the section that keeps the rest of the page honest. A boundary drawn
// over unmeasured releases, a cause nothing named, a tree nobody resolved —
// each of those is a blank a reader would otherwise fill in themselves, and
// the alternative to saying so is not neutrality, it is a page that reads as
// more certain than its evidence.
func failureIssueGaps(lang string, issue failureIssue,
	verdicts map[string]failureIssueVerdict, boundaries []failureBoundary) []string {

	var out []string
	if issue.EvidenceGap {
		out = append(out, i18n.T(lang, "issue.gap_unproven"))
	}
	if len(issue.Versions) == 0 {
		out = append(out, i18n.T(lang, "issue.gap_no_release"))
	}
	if issue.EvidenceGapKind != "" {
		out = append(out, i18n.T(lang, "issue.gap_kind", issue.EvidenceGapKind))
	}
	var passes, unmeasured int
	for _, v := range verdicts {
		switch v {
		case issueVerdictPass:
			passes++
		case issueVerdictUnmeasured:
			unmeasured++
		}
	}
	if passes == 0 && len(verdicts) > 0 {
		out = append(out, i18n.T(lang, "issue.gap_no_pass", issue.Stage))
	}
	if unmeasured > 0 {
		out = append(out, i18n.T(lang, "issue.gap_unmeasured",
			issue.Stage, strconv.Itoa(unmeasured)))
	}
	for _, b := range boundaries {
		if b.TreeUnread {
			out = append(out, i18n.T(lang, "issue.gap_tree_unread",
				b.PassVersion, b.FailVersion))
			break
		}
	}
	if len(issue.Hypotheses) == 0 {
		out = append(out, i18n.T(lang, "issue.gap_no_cause"))
	}
	return out
}

func verdictKey(v failureIssueVerdict) string {
	switch v {
	case issueVerdictPass:
		return "pass"
	case issueVerdictFail:
		return "fail"
	}
	return "unmeasured"
}

// verdictTone picks from the chip vocabulary the stylesheet defines, so a
// verdict is coloured by the same rules as every other chip on the site.
func verdictTone(v failureIssueVerdict) string {
	switch v {
	case issueVerdictPass:
		return "high"
	case issueVerdictFail:
		return "elevated"
	}
	return "unknown"
}

func verdictLabel(lang string, v failureIssueVerdict) string {
	switch v {
	case issueVerdictPass:
		return i18n.T(lang, "issue.verdict_pass")
	case issueVerdictFail:
		return i18n.T(lang, "issue.verdict_fail")
	}
	return i18n.T(lang, "issue.verdict_unmeasured")
}

// failureIssueDescription says what THIS issue is, not what the site is. A
// description shared by every failure page is a description a search result
// cannot be told apart by.
func failureIssueDescription(lang, name string, issue failureIssue) string {
	signature := issue.Stage
	if issue.ErrorCode != "" {
		signature += " " + issue.ErrorCode
	} else if issue.Termination != "" {
		signature += " " + issue.Termination
	}
	releases := "—"
	if len(issue.Versions) > 0 {
		releases = issue.Versions[0]
		if len(issue.Versions) > 1 {
			releases = issue.Versions[len(issue.Versions)-1] + " … " + issue.Versions[0]
		}
	}
	return i18n.T(lang, "issue.meta", name, signature, releases)
}
