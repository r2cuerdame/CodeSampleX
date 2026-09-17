package web

import (
	"fmt"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// The verification frontier, as a release page shows it.
//
// The builder publishes three products off the same signed receipts:
// where a verdict changes between adjacent measured releases (a boundary),
// how far up an unbroken run of measured passes reaches from one release (a
// safe upgrade), and which release's verdict changed over time and wants
// revalidating (a regression watch). They ride on the snapshot document,
// which the registry API already serves whole; this is the same content
// read by a person.
//
// Two rules govern the rendering. A measured boundary and an inferred
// candidate are never dressed alike: the first is two executions of one
// immutable case under one set of conditions, the second is a fail-rate
// shift in anonymous observations, and only the first is a fact about a
// release. And every line carries the conditions it was measured under,
// because a path proven for one adapter on one OS is not a package-wide
// promise, and a reader who cannot see the conditions will make it one.

// frontierBoundary is the snapshot's regressionCandidates entry. A receipt-
// established boundary carries a caseId and the CONTRACT stage; an
// observation-based candidate carries neither.
type frontierBoundary struct {
	Package              string   `json:"package"`
	PreviousPackage      string   `json:"previousPackage"`
	CaseID               string   `json:"caseId"`
	Symbol               string   `json:"symbol"`
	VerifierAdapter      string   `json:"verifierAdapter"`
	SandboxCapability    string   `json:"sandboxCapability"`
	CompanionPackages    []string `json:"companionPackages"`
	Stage                string   `json:"stage"`
	ContextLabel         string   `json:"contextLabel"`
	FailRate             float64  `json:"failRate"`
	PreviousPassRate     float64  `json:"previousPassRate"`
	Observations         int64    `json:"observations"`
	PreviousObservations int64    `json:"previousObservations"`
}

// frontierUpgrade is the snapshot's safeUpgradeCandidates entry.
type frontierUpgrade struct {
	Package             string   `json:"package"`
	TargetPackage       string   `json:"targetPackage"`
	CaseID              string   `json:"caseId"`
	Symbol              string   `json:"symbol"`
	CurrentResult       string   `json:"currentResult"`
	VerifierAdapter     string   `json:"verifierAdapter"`
	SandboxCapability   string   `json:"sandboxCapability"`
	CompanionPackages   []string `json:"companionPackages"`
	ContextLabel        string   `json:"contextLabel"`
	MeasuredPath        []string `json:"measuredPath"`
	BlockedByVersion    string   `json:"blockedByVersion"`
	CurrentObservations int64    `json:"currentObservations"`
	TargetObservations  int64    `json:"targetObservations"`
}

// frontierWatch is the snapshot's regressionWatchCandidates entry.
type frontierWatch struct {
	Package                 string   `json:"package"`
	CaseID                  string   `json:"caseId"`
	Symbol                  string   `json:"symbol"`
	Kind                    string   `json:"kind"`
	VerifierAdapter         string   `json:"verifierAdapter"`
	SandboxCapability       string   `json:"sandboxCapability"`
	CompanionPackages       []string `json:"companionPackages"`
	ContextLabel            string   `json:"contextLabel"`
	KnownResult             string   `json:"knownResult"`
	KnownObservations       int64    `json:"knownObservations"`
	KnownThrough            string   `json:"knownThrough"`
	LatestResult            string   `json:"latestResult"`
	LatestObservations      int64    `json:"latestObservations"`
	LatestSince             string   `json:"latestSince"`
	NearestLowerPassPackage string   `json:"nearestLowerPassPackage"`
}

// frontierRow is one rendered line of the frontier list.
type frontierRow struct {
	// Class picks the chip: measured | inferred | upgrade | watch.
	Class string
	// Kicker is the translated chip text naming what kind of claim this is.
	Kicker string
	// Text is the claim itself, with the other release named in it.
	Text string
	// Href and Label link the other release the claim names: the pass
	// below a boundary, an upgrade's target, a watch's nearest lower pass.
	// Empty when the claim names none.
	Href  string
	Label string
	// Detail is a second line: a measured path, or what stopped it.
	Detail string
	// Symbol is the API the claim is about, empty for package-level claims.
	Symbol string
	// Conditions are the comparison dimensions every measurement on the
	// line was taken under, rendered as chips.
	Conditions []string
	// Runs counts the receipts behind each side of the claim.
	Runs string
}

// frontierView is what the version and symbol pages render.
type frontierView struct {
	Rows []frontierRow
}

// buildFrontier reads the frontier products out of every snapshot the page
// decoded -- the package-level document and one per symbol -- and renders
// them in a fixed order: measured boundaries, safe upgrades, regression
// watch, then inferred candidates last and visibly apart.
func buildFrontier(lang, eco, name string, docs []snapshotDoc) frontierView {
	var view frontierView
	seen := map[string]bool{}
	var measured, inferred, upgrades, watch []frontierRow
	for _, doc := range docs {
		for _, c := range doc.RegressionCandidates {
			key := "b\x00" + c.Symbol + "\x00" + c.PreviousPackage + "\x00" + c.CaseID + "\x00" + c.ContextLabel + "\x00" + c.VerifierAdapter + "\x00" + c.Stage
			if seen[key] {
				continue
			}
			seen[key] = true
			row := boundaryRow(lang, eco, name, c)
			if row.Class == "measured" {
				measured = append(measured, row)
			} else {
				inferred = append(inferred, row)
			}
		}
		for _, c := range doc.SafeUpgradeCandidates {
			key := "u\x00" + c.Symbol + "\x00" + c.TargetPackage + "\x00" + c.CaseID + "\x00" + c.ContextLabel + "\x00" + c.VerifierAdapter
			if seen[key] {
				continue
			}
			seen[key] = true
			upgrades = append(upgrades, upgradeRow(lang, eco, name, c))
		}
		for _, c := range doc.RegressionWatchCandidates {
			key := "w\x00" + c.Symbol + "\x00" + c.CaseID + "\x00" + c.ContextLabel + "\x00" + c.VerifierAdapter
			if seen[key] {
				continue
			}
			seen[key] = true
			watch = append(watch, watchRow(lang, eco, name, c))
		}
	}
	view.Rows = append(view.Rows, measured...)
	view.Rows = append(view.Rows, upgrades...)
	view.Rows = append(view.Rows, watch...)
	view.Rows = append(view.Rows, inferred...)
	return view
}

func boundaryRow(lang, eco, name string, c frontierBoundary) frontierRow {
	prev := purlVersion(c.PreviousPackage)
	row := frontierRow{
		Symbol: c.Symbol,
		Href:   versionHref(eco, name, prev), Label: prev,
		Runs: i18n.T(lang, "frontier.runs", i18n.FormatInt(lang, c.PreviousObservations),
			i18n.FormatInt(lang, c.Observations)),
	}
	if c.CaseID != "" && c.Stage == string(domain.StageContract) {
		row.Class, row.Kicker = "measured", i18n.T(lang, "frontier.measured_boundary")
		row.Text = i18n.T(lang, "frontier.boundary_text")
		row.Conditions = frontierConditions(c.ContextLabel, c.VerifierAdapter, c.SandboxCapability, c.CaseID, c.CompanionPackages)
		return row
	}
	// An observation candidate is a rate, not a verdict. It says so, keeps
	// the stage it was counted at, and never borrows the measured chip.
	row.Class, row.Kicker = "inferred", i18n.T(lang, "frontier.inferred_candidate")
	row.Text = i18n.T(lang, "frontier.inferred_text",
		percent(c.FailRate), percent(c.PreviousPassRate))
	row.Conditions = frontierConditions(c.ContextLabel, "", "", "", nil)
	if c.Stage != "" {
		row.Conditions = append(row.Conditions, c.Stage)
	}
	return row
}

func upgradeRow(lang, eco, name string, c frontierUpgrade) frontierRow {
	target := purlVersion(c.TargetPackage)
	row := frontierRow{
		Class: "upgrade", Kicker: i18n.T(lang, "frontier.safe_upgrade"),
		Symbol: c.Symbol,
		Href:   versionHref(eco, name, target), Label: target,
		Conditions: frontierConditions(c.ContextLabel, c.VerifierAdapter, c.SandboxCapability, c.CaseID, c.CompanionPackages),
		Runs: i18n.T(lang, "frontier.runs", i18n.FormatInt(lang, c.CurrentObservations),
			i18n.FormatInt(lang, c.TargetObservations)),
	}
	// An escape off a failing release and a step up from a working one are
	// different suggestions, and they must not read alike.
	if c.CurrentResult == string(domain.ResultFail) {
		row.Text = i18n.T(lang, "frontier.upgrade_recovery_text")
	} else {
		row.Text = i18n.T(lang, "frontier.upgrade_text")
	}
	path := i18n.T(lang, "frontier.upgrade_path", strings.Join(c.MeasuredPath, " → "))
	switch {
	case c.BlockedByVersion != "":
		row.Detail = path + " · " + i18n.T(lang, "frontier.upgrade_blocked", c.BlockedByVersion)
	default:
		// Nothing above the target carries a verdict. That is the edge of
		// the evidence, not a claim that the target is the newest release.
		row.Detail = path + " · " + i18n.T(lang, "frontier.upgrade_open")
	}
	return row
}

func watchRow(lang, eco, name string, c frontierWatch) frontierRow {
	row := frontierRow{
		Class: "watch", Kicker: i18n.T(lang, "frontier.watch"),
		Symbol:     c.Symbol,
		Conditions: frontierConditions(c.ContextLabel, c.VerifierAdapter, c.SandboxCapability, c.CaseID, c.CompanionPackages),
		Runs: i18n.T(lang, "frontier.runs", i18n.FormatInt(lang, c.KnownObservations),
			i18n.FormatInt(lang, c.LatestObservations)),
	}
	args := []any{
		i18n.FormatInt(lang, c.KnownObservations), datePart(c.KnownThrough),
		i18n.FormatInt(lang, c.LatestObservations), datePart(c.LatestSince),
	}
	if c.Kind == "KNOWN_FAILURE_NOW_PASSING" {
		row.Text = i18n.T(lang, "frontier.watch_failure_text", args...)
	} else {
		row.Text = i18n.T(lang, "frontier.watch_good_text", args...)
	}
	// The nearest pass below is history, not a suggestion: it is where the
	// boundary this crosses used to rest. It goes on the detail line, not
	// in the link slot a suggestion would use.
	if lower := purlVersion(c.NearestLowerPassPackage); lower != "" {
		row.Detail = i18n.T(lang, "frontier.watch_lower_pass", lower)
	}
	return row
}

// frontierConditions renders the comparison dimensions as chips, in a fixed
// order, skipping what a candidate did not carry.
func frontierConditions(contextLabel, adapter, sandbox, caseID string, companions []string) []string {
	var out []string
	if contextLabel != "" {
		out = append(out, contextLabel)
	}
	if adapter != "" {
		out = append(out, adapter)
	}
	if sandbox != "" {
		out = append(out, sandbox)
	}
	if caseID != "" {
		out = append(out, shortCaseID(caseID))
	}
	for _, p := range companions {
		out = append(out, purlShort(p))
	}
	return out
}

// purlVersion is the version part of a purl, or "" when it has none.
func purlVersion(purl string) string {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return ""
	}
	return p.Version
}

// purlShort is "name@version" for a companion chip; the ecosystem is the
// page's own and would only repeat.
func purlShort(purl string) string {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return purl
	}
	if p.Version == "" {
		return p.Name
	}
	return p.Name + "@" + p.Version
}

// shortCaseID keeps a case identifier readable as a chip. Case ids are
// content addresses; a prefix identifies the case to a reader who has the
// sample page open and stays short enough to sit on one line.
func shortCaseID(id string) string {
	const keep = 12
	if strings.HasPrefix(id, "case:") && len(id) > len("case:")+keep {
		return id[:len("case:")+keep] + "…"
	}
	if len(id) > keep+1 {
		return id[:keep] + "…"
	}
	return id
}

func percent(rate float64) string {
	return fmt.Sprintf("%d%%", int(rate*100+0.5))
}
