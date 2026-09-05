package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// depHref points at one library at one exact version — the same pinned view
// this list itself lives on.
func depHref(ecosystem, name, version string) string {
	return pkgHref(ecosystem, name) + "?f_version=" + url.QueryEscape(version)
}

// DependencyHealthSummary summarizes the health of dependencies for a release (#178).
type DependencyHealthSummary struct {
	ProblemsCount int
	MixedCount    int
	ChangedCount  int
	SteadyCount   int
	UnknownCount  int
	TotalCount    int

	HasBreak   bool
	FirstBreak *DependencyBreakDetail
}

// DependencyBreakDetail captures the first observed break linked to this dependency tree.
type DependencyBreakDetail struct {
	ParentVersion string
	ChildLibrary  string
	ChildVersion  string
	Env           string
	FailCount     int64
	PassCount     int64
	Stage         string
	Fingerprint   string
	CubeHref      string
	FailureHref   string
}

// PackageDep is one first-level dependency of ONE release, at the version
// that release resolved it to.
type PackageDep struct {
	Library string
	Version string
	// Href is that library at that exact version.
	Href string
	// AtlasHref is the same release read from the other side.
	AtlasHref string
	Projects     int64
	ProjectsText string
	// State is what this network has MEASURED about the child release itself.
	State     string
	StateText string

	// Health diagnostics (#178)
	Health      string // "fail", "mixed", "candidate", "changed", "pass", "unknown"
	HealthBadge string // "FAIL", "MIXED", "CANDIDATE", "CHANGED", "PASS", "UNKNOWN"
	HealthTone  string // "red", "yellow", "blue", "green", "dim"
	HealthNote  string
	IsMover     bool
	SameReceipt bool
	Outcome     string
}

// buildPackageDeps lists what one pinned release pulled.
//
// This is shown only with a version pinned, and that is the point. Across
// releases the same library appears at several versions, so a page covering
// every release has to choose which to display — a choice nobody asked it to
// make and one a reader cannot check. Pinned, there is exactly one answer.
//
// A library resolved at two versions under a single release is kept as two
// rows rather than collapsed: that is the collision worth seeing, and picking
// one would hide exactly it.
func buildPackageDeps(ecosystem string, edges []DependencyEdge) []PackageDep {
	// Keyed by coordinate rather than by the whole struct: the same pair can
	// arrive on several rows and their project counts have to add up, which a
	// set of structs cannot do.
	type coord struct{ library, version string }
	projects := map[coord]int64{}
	sameReceipt := map[coord]bool{}
	outcome := map[coord]string{}
	for _, e := range edges {
		if e.ChildName == "" || e.ChildVersion == "" {
			continue
		}
		c := coord{e.ChildName, e.ChildVersion}
		projects[c] += e.Projects
		if e.SameReceipt {
			sameReceipt[c] = true
			if e.Outcome != "" {
				if prev := outcome[c]; prev != "" && prev != e.Outcome {
					outcome[c] = "mixed"
				} else {
					outcome[c] = e.Outcome
				}
			}
		}
	}
	out := make([]PackageDep, 0, len(projects))
	for c, n := range projects {
		out = append(out, PackageDep{
			Library:     c.library,
			Version:     c.version,
			Href:        depHref(ecosystem, c.library, c.version),
			AtlasHref:   dependencyAtlasHref(ecosystem, c.library, c.version),
			Projects:    n,
			SameReceipt: sameReceipt[c],
			Outcome:     outcome[c],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Library != out[j].Library {
			return out[i].Library < out[j].Library
		}
		return out[i].Version < out[j].Version
	})
	return out
}

// maxDependencyRows bounds how many dependency rows one release renders, and
// therefore how many snapshot reads a page makes. A first-level dependency
// list is a handful for most releases and a few dozen for a framework; past
// that a reader is not reading rows, they are scrolling past them.
const maxDependencyRows = 40

// dependencyEvidenceState says what this network measured about one child
// release — never about the edge that led to it.
//
//	verified  a contract ran at that coordinate and was recorded
//	observed  builds were seen there, but no contract has run
//	none      nothing has been measured at that coordinate at all
//
// "none" is a gap, not a verdict. It is the state most dependency rows are in,
// and saying so plainly is the point: the alternative is a blank cell a reader
// fills in with an assumption.
func dependencyEvidenceState(r *http.Request, store Store, purl string) string {
	raw, ok := store.SnapshotJSON(r.Context(), purl, "")
	if !ok || raw == "" {
		return "none"
	}
	var doc snapshotDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return "none"
	}
	var observed, verified int64
	for _, row := range doc.Rows {
		sp := splitStageCounts(row.ByStage)
		observed += sp.obs + sp.used
		verified += sp.ver
	}
	switch {
	case verified > 0:
		return "verified"
	case observed > 0:
		return "observed"
	default:
		return "none"
	}
}

func formatClusterEnv(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	parts := make([]string, 0, len(env))
	for _, key := range []string{"os", "runtime", "packageManager", "execution", "libc", "arch"} {
		if val, ok := env[key]; ok && val != "" {
			parts = append(parts, val)
		}
	}
	for k, v := range env {
		if k == "os" || k == "runtime" || k == "packageManager" || k == "execution" || k == "libc" || k == "arch" {
			continue
		}
		if v != "" {
			parts = append(parts, k+"="+v)
		}
	}
	return strings.Join(parts, " · ")
}

// exactConditionsHref builds the deep link restoring the exact coordinate of a break
// including version and all relevant environment coordinates (os, runtime, context, etc.).
func exactConditionsHref(eco, name, parentVersion string, fc failureCluster) string {
	q := url.Values{}
	if parentVersion != "" {
		q.Set("f_version", parentVersion)
	}
	if fc.Symbol != "" {
		q.Set("f_symbol", fc.Symbol)
	}
	if len(fc.EnvSummary) > 0 {
		if v := fc.EnvSummary["os"]; v != "" {
			q.Set("f_os", v)
		}
		if v := fc.EnvSummary["runtime"]; v != "" {
			q.Set("f_runtime", v)
		}
		if v := fc.EnvSummary["executionContext"]; v != "" {
			q.Set("f_context", v)
		} else if v := fc.EnvSummary["context"]; v != "" {
			q.Set("f_context", v)
		}
		if v := fc.EnvSummary["arch"]; v != "" {
			q.Set("f_arch", v)
		}
		if v := fc.EnvSummary["libc"]; v != "" {
			q.Set("f_libc", v)
		}
		if v := fc.EnvSummary["packageManager"]; v != "" {
			q.Set("f_tool", v)
		} else if v := fc.EnvSummary["tool"]; v != "" {
			q.Set("f_tool", v)
		}
		var otherKeys []string
		for k := range fc.EnvSummary {
			switch k {
			case "os", "runtime", "executionContext", "context", "arch", "libc", "packageManager", "tool":
				// already mapped
			default:
				otherKeys = append(otherKeys, k)
			}
		}
		sort.Strings(otherKeys)
		for _, k := range otherKeys {
			if v := fc.EnvSummary[k]; v != "" {
				q.Set("f_"+k, v)
			}
		}
	}
	return pkgHref(eco, name) + "?" + q.Encode() + cubeAnchor
}

// evaluateDependencyHealth computes combination and regression health for the
// dependencies of parentVersion (#178).
func evaluateDependencyHealth(
	eco, name, parentVersion string,
	deps []PackageDep,
	clusters []failureCluster,
	matrix *dependencyMatrix,
	lang string,
) ([]PackageDep, *DependencyHealthSummary) {
	summary := &DependencyHealthSummary{
		TotalCount: len(deps),
	}
	if len(deps) == 0 {
		return deps, summary
	}

	moverMap := map[string]bool{}
	if matrix != nil {
		for _, row := range matrix.Rows {
			if row.Moves {
				moverMap[row.Child] = true
			}
		}
	}

	var parentFailures []failureCluster
	var totalFailCount, totalObsCount int64
	for _, c := range clusters {
		applies := false
		if len(c.Versions) == 0 {
			applies = true
		} else {
			for _, v := range c.Versions {
				if v == parentVersion {
					applies = true
					break
				}
			}
		}
		if applies {
			parentFailures = append(parentFailures, c)
			totalFailCount += c.Count
			totalObsCount += c.ObservationCount
		}
	}

	hasParentFailure := len(parentFailures) > 0

	for i := range deps {
		d := &deps[i]
		d.IsMover = moverMap[d.Library]

		// 1. Same-receipt proof on this exact parent+child combination.
		// Parent-level failure must NOT be attributed unless the same receipt/execution
		// proves that exact parent+child combination (#178).
		if d.SameReceipt && d.Outcome != "" {
			switch d.Outcome {
			case "fail":
				d.Health = "fail"
				d.HealthBadge = i18n.T(lang, "pkg.dh_badge_fail")
				d.HealthTone = "red"
				if d.IsMover {
					d.HealthNote = i18n.T(lang, "pkg.dh_note_fail")
				} else {
					d.HealthNote = i18n.T(lang, "pkg.dh_note_fail_unmoved")
				}
				summary.ProblemsCount++
			case "mixed":
				d.Health = "mixed"
				d.HealthBadge = i18n.T(lang, "pkg.dh_badge_mixed")
				d.HealthTone = "yellow"
				d.HealthNote = i18n.T(lang, "pkg.dh_note_mixed")
				summary.MixedCount++
			case "pass":
				d.Health = "pass"
				d.HealthBadge = i18n.T(lang, "pkg.dh_badge_pass")
				d.HealthTone = "green"
				d.HealthNote = i18n.T(lang, "pkg.dh_note_pass")
				summary.SteadyCount++
			default:
				d.Health = "unknown"
				d.HealthBadge = i18n.T(lang, "pkg.dh_badge_unknown")
				d.HealthTone = "dim"
				d.HealthNote = i18n.T(lang, "pkg.dh_note_unknown")
				summary.UnknownCount++
			}
			continue
		}

		// 2. Cross-receipt / no same-receipt proof.
		// Parent-level failure is not attributed to dependencies without same-receipt proof.
		// Preserves honest UNKNOWN / CANDIDATE semantics.
		if hasParentFailure && d.IsMover {
			// Version changed under parent failure without same-receipt proof:
			// Correlated candidate, NOT confirmed failure.
			d.Health = "candidate"
			d.HealthBadge = i18n.T(lang, "pkg.dh_badge_candidate")
			d.HealthTone = "yellow"
			d.HealthNote = i18n.T(lang, "pkg.dh_note_candidate")
			summary.ChangedCount++
		} else if d.IsMover {
			// Version changed across releases without parent failure.
			d.Health = "changed"
			d.HealthBadge = i18n.T(lang, "pkg.dh_badge_changed")
			d.HealthTone = "blue"
			d.HealthNote = i18n.T(lang, "pkg.dh_note_changed")
			summary.ChangedCount++
		} else if d.State == "verified" || d.State == "observed" {
			// Unmoved dependency with passing / observed child measurements.
			d.Health = "pass"
			d.HealthBadge = i18n.T(lang, "pkg.dh_badge_pass")
			d.HealthTone = "green"
			d.HealthNote = i18n.T(lang, "pkg.dh_note_pass")
			summary.SteadyCount++
		} else {
			// Unmoved dependency without measurements.
			d.Health = "unknown"
			d.HealthBadge = i18n.T(lang, "pkg.dh_badge_unknown")
			d.HealthTone = "dim"
			d.HealthNote = i18n.T(lang, "pkg.dh_note_unknown")
			summary.UnknownCount++
		}
	}

	if hasParentFailure {
		fc := parentFailures[0]
		summary.HasBreak = true
		if summary.ProblemsCount == 0 {
			summary.ProblemsCount = 1
		}
		b := &DependencyBreakDetail{
			ParentVersion: parentVersion,
			Env:           formatClusterEnv(fc.EnvSummary),
			FailCount:     fc.Count,
			PassCount:     fc.ObservationCount - fc.Count,
			Stage:         fc.Stage,
			Fingerprint:   fc.Fingerprint,
			CubeHref:      exactConditionsHref(eco, name, parentVersion, fc),
			FailureHref:   "#failures",
		}
		if b.PassCount < 0 {
			b.PassCount = 0
		}
		for _, d := range deps {
			if d.Health == "fail" && d.SameReceipt {
				b.ChildLibrary = d.Library
				b.ChildVersion = d.Version
				break
			}
		}
		summary.FirstBreak = b
	}

	sort.Slice(deps, func(i, j int) bool {
		rank := func(h string) int {
			switch h {
			case "fail":
				return 1
			case "mixed":
				return 2
			case "candidate":
				return 3
			case "changed":
				return 4
			case "unknown":
				return 5
			case "pass":
				return 6
			default:
				return 7
			}
		}
		ri, rj := rank(deps[i].Health), rank(deps[j].Health)
		if ri != rj {
			return ri < rj
		}
		if deps[i].Library != deps[j].Library {
			return deps[i].Library < deps[j].Library
		}
		return deps[i].Version < deps[j].Version
	})

	return deps, summary
}

// evaluateCrossReleaseHealth produces a problem-first summary across all observed
// releases when no single version is pinned (#178).
func evaluateCrossReleaseHealth(
	ecosystem, name string,
	clusters []failureCluster,
	matrix *dependencyMatrix,
	lang string,
) *DependencyHealthSummary {
	if matrix == nil {
		return nil
	}
	summary := &DependencyHealthSummary{
		ChangedCount: matrix.Moved,
		SteadyCount:  matrix.Steady,
	}

	if len(clusters) > 0 {
		fc := clusters[0]
		summary.HasBreak = true
		summary.ProblemsCount = 1
		pVer := ""
		if len(fc.Versions) > 0 {
			pVer = fc.Versions[0]
		} else if len(matrix.Versions) > 0 {
			pVer = matrix.Versions[0]
		}
		b := &DependencyBreakDetail{
			ParentVersion: pVer,
			Env:           formatClusterEnv(fc.EnvSummary),
			FailCount:     fc.Count,
			PassCount:     fc.ObservationCount - fc.Count,
			Stage:         fc.Stage,
			Fingerprint:   fc.Fingerprint,
			CubeHref:      exactConditionsHref(ecosystem, name, pVer, fc),
			FailureHref:   "#failures",
		}
		if b.PassCount < 0 {
			b.PassCount = 0
		}
		summary.FirstBreak = b
	}

	return summary
}

