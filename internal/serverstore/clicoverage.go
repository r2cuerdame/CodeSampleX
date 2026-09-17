package serverstore

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The CLI coverage planner: which (tool, version, command, OS) coordinates
// the farm should run next, and which it cannot.
//
// A CLI subject reaches the server as an ordinary observation batch keyed by
// the public generic purl pkg:generic/cli/<tool>@<version>, with the command
// pattern in the symbol behind a provenance prefix ("field:" for a user's
// machine, "farm:" for the farm) and the OS in the environment. That is the
// whole subject on the wire, and the gap is every cell of that matrix
// nobody has observed.
//
// The planner is pure: it reads a bounded list of those rows plus the Wanted
// asks and produces two things from one pass -- the work rows the authoring
// funnel offers (Kind "CLI", Axis EVIDENCE, the OS carried in the symbol by
// domain.EncodeCLIWorkSymbol) and a census that says, per tool, what is
// observed, what is queued and what this farm cannot reach. Both stores feed
// it the same rows, so there is one implementation of the rules.
//
// What it will not do is guess. The farm runs a command with whatever
// version of the tool its image ships, so the version a command-level gap is
// filed at is the version the farm has actually been measured to have on
// that OS -- learned by a tool-level probe, which is the first work for any
// tool. A coordinate on an OS the farm has no lane for, or at a version the
// farm's OS does not provide, is classified and counted rather than queued:
// handed out, it would come back as a failure of the tool, which it is not.

// CLIObservationRow is one evidence_agg row about a CLI subject, as stored.
type CLIObservationRow struct {
	// PURL is pkg:generic/cli/<tool>@<version>.
	PURL string
	// Symbol is the evidence symbol as recorded: "<provenance>:<command>".
	Symbol string
	// OS is the environment's os, lower-cased.
	OS       string
	Result   string
	Count    int64
	LastSeen time.Time
}

// CLIObservationStore reads the CLI slice of the evidence corpus. It is small
// -- every row is about one of a few dozen fixed public tools -- which is
// why the planner can be a Go pass over it rather than a query per axis.
type CLIObservationStore interface {
	ListCLIObservations(ctx context.Context, limit int) ([]CLIObservationRow, error)
}

const (
	// CLIObservationReadLimit bounds the rows the planner reads. Production
	// holds a few hundred CLI rows; this is a ceiling, not a target.
	CLIObservationReadLimit = 20000

	// CLICommandsPerToolOS caps how many command-level gaps one (tool, OS)
	// may contribute to a plan. Uncapped, one busy tool's command history
	// fills the window and every other tool waits behind it.
	CLICommandsPerToolOS = 12

	// CLIFarmReprobeAfter is how long a tool-level probe stays the farm's
	// version of record for an OS. Images move; after this, the tool is
	// probed again before more command-level gaps are filed at the old
	// version.
	CLIFarmReprobeAfter = 30 * 24 * time.Hour

	// cliFailureWeight is what one recorded failure row of a command is
	// worth: a failure nobody has explained is the question the farm can
	// answer best, so it leads. cliBoundaryWeight is a command whose verdict
	// flipped between two versions. Both sit below an explicit ask
	// (authoringDirectWeight) and above any volume of passes.
	cliFailureWeight  = 1000
	cliBoundaryWeight = 500
)

// CLIToolCoverage is one tool's row of the census.
type CLIToolCoverage struct {
	Tool string
	Seed bool
	// Observed counts distinct (version, command, OS) coordinates any
	// provenance has recorded; FarmObserved the subset the farm recorded.
	Observed     int
	FarmObserved int
	// Queued is how many rows this tool contributed to the plan.
	Queued int
	// Unavailable counts coordinates this farm cannot reach: an OS it has no
	// lane for, or an asked version its OS does not provide.
	Unavailable int
	// FarmVersions is the version the farm has been measured to have, per
	// OS. An OS absent here has not been probed, or its probe is stale.
	FarmVersions map[string]string
	// Failures counts commands with at least one FAIL row; Boundaries the
	// commands whose verdict differs between adjacent observed versions.
	Failures   int
	Boundaries int
}

// CLICompleteness is the census the plan was drawn from.
type CLICompleteness struct {
	FarmOS       []string
	Tools        []CLIToolCoverage
	Observed     int
	FarmObserved int
	Queued       int
	Unavailable  int
	// Unavailability counts the unreachable coordinates by reason, in the
	// words the panel prints.
	Unavailability map[string]int
}

func (c CLICompleteness) toolNamed(tool string) CLIToolCoverage {
	for _, t := range c.Tools {
		if t.Tool == tool {
			return t
		}
	}
	return CLIToolCoverage{}
}

// CLICoveragePlan is what the funnel offers and what the panel shows.
type CLICoveragePlan struct {
	Gaps   []WantedRow
	Census CLICompleteness
}

// cliCoord is one cell of the matrix.
type cliCoord struct {
	tool, version, command, os string
}

type cliCommandStats struct {
	count    int64
	failRows int
	// verdicts per version: PASS-only, FAIL-only or mixed.
	pass, fail map[string]bool
}

// parseCLIObservation splits a stored row into its coordinate and provenance.
func parseCLIObservation(row CLIObservationRow) (coord cliCoord, farm bool, ok bool) {
	p, err := domain.ParsePURL(row.PURL)
	if err != nil || p.Ecosystem != "generic" || p.Version == "" {
		return cliCoord{}, false, false
	}
	tool, ok := domain.CLIToolFromTargetName(p.Name)
	if !ok {
		return cliCoord{}, false, false
	}
	command := row.Symbol
	switch {
	case strings.HasPrefix(command, "farm:"):
		farm = true
		command = command[len("farm:"):]
	case strings.HasPrefix(command, "field:"):
		command = command[len("field:"):]
	}
	return cliCoord{
		tool: tool, version: p.Version,
		command: strings.Join(strings.Fields(command), " "),
		os:      strings.ToLower(strings.TrimSpace(row.OS)),
	}, farm, true
}

// cliWantedCommand normalizes a Wanted symbol to the command pattern the
// evidence would carry: the tool's own name is dropped from the front when
// the reporter included it ("gh run watch <arg>" and "run watch <arg>" are
// one ask).
func cliWantedCommand(tool, symbol string) string {
	fields := strings.Fields(symbol)
	if len(fields) > 0 && domain.CommandTool(fields[:1]) == tool {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

// PlanCLICoverage computes the CLI gap plan from the CLI evidence rows, the
// Wanted asks and the OS set the farm can run commands on.
func PlanCLICoverage(observations []CLIObservationRow, wanted []WantedRow, farmOS []string, now time.Time, limit int) CLICoveragePlan {
	farm := make(map[string]bool, len(farmOS))
	farmList := make([]string, 0, len(farmOS))
	for _, os := range farmOS {
		os = strings.ToLower(strings.TrimSpace(os))
		if os == "" || farm[os] {
			continue
		}
		farm[os] = true
		farmList = append(farmList, os)
	}
	sort.Strings(farmList)

	observed := map[cliCoord]bool{}
	farmObserved := map[cliCoord]bool{}
	// The farm's version of record per (tool, os): the newest farm row.
	type farmProbe struct {
		version string
		at      time.Time
	}
	farmVersion := map[[2]string]farmProbe{}
	commands := map[string]map[string]*cliCommandStats{}
	seen := map[string]bool{}
	for _, row := range observations {
		coord, isFarm, ok := parseCLIObservation(row)
		if !ok {
			continue
		}
		seen[coord.tool] = true
		observed[coord] = true
		if isFarm {
			farmObserved[coord] = true
			key := [2]string{coord.tool, coord.os}
			if prev, ok := farmVersion[key]; !ok || row.LastSeen.After(prev.at) {
				farmVersion[key] = farmProbe{version: coord.version, at: row.LastSeen}
			}
		}
		if coord.command == "" {
			continue
		}
		if commands[coord.tool] == nil {
			commands[coord.tool] = map[string]*cliCommandStats{}
		}
		stats := commands[coord.tool][coord.command]
		if stats == nil {
			stats = &cliCommandStats{pass: map[string]bool{}, fail: map[string]bool{}}
			commands[coord.tool][coord.command] = stats
		}
		stats.count += row.Count
		switch row.Result {
		case string(domain.ResultFail):
			stats.failRows++
			stats.fail[coord.version] = true
		case string(domain.ResultPass):
			stats.pass[coord.version] = true
		}
	}

	// Asks, keyed by the coordinate they name. An ask without an OS is an
	// ask on every farm OS.
	type cliAsk struct {
		coord cliCoord
		asks  int64
	}
	var asks []cliAsk
	for _, w := range wanted {
		if w.Ecosystem != "generic" || w.Version == "" {
			continue
		}
		tool, ok := domain.CLIToolFromTargetName(w.Name)
		if !ok {
			continue
		}
		seen[tool] = true
		command := cliWantedCommand(tool, w.Symbol)
		os := strings.ToLower(strings.TrimSpace(w.TargetOS))
		if os != "" {
			asks = append(asks, cliAsk{cliCoord{tool, w.Version, command, os}, w.Asks})
			continue
		}
		for _, os := range farmList {
			asks = append(asks, cliAsk{cliCoord{tool, w.Version, command, os}, w.Asks})
		}
	}

	census := CLICompleteness{FarmOS: farmList, Unavailability: map[string]int{}}
	unavailable := map[cliCoord]string{}
	classifyUnavailable := func(coord cliCoord, reason string) {
		if _, done := unavailable[coord]; done {
			return
		}
		unavailable[coord] = reason
	}
	for coord := range observed {
		if !farm[coord.os] {
			classifyUnavailable(coord, "no farm lane for "+coord.os)
		}
	}

	tools := make([]string, 0, len(seen)+len(domain.CLISeedTools))
	isTool := map[string]bool{}
	for _, tool := range domain.CLISeedTools {
		tools = append(tools, tool)
		isTool[tool] = true
	}
	for tool := range seen {
		if !isTool[tool] {
			tools = append(tools, tool)
			isTool[tool] = true
		}
	}
	sort.SliceStable(tools[len(domain.CLISeedTools):], func(i, j int) bool {
		return tools[len(domain.CLISeedTools)+i] < tools[len(domain.CLISeedTools)+j]
	})

	var gaps []WantedRow
	for _, tool := range tools {
		seedRank, seeded := domain.CLISeedRank(tool)
		cov := CLIToolCoverage{Tool: tool, Seed: seeded, FarmVersions: map[string]string{}}
		for coord := range observed {
			if coord.tool == tool {
				cov.Observed++
				if farmObserved[coord] {
					cov.FarmObserved++
				}
			}
		}
		for command, stats := range commands[tool] {
			if stats.failRows > 0 {
				cov.Failures++
			}
			if cliVersionBoundary(stats) {
				cov.Boundaries++
			}
			_ = command
		}
		var toolGaps []WantedRow
		for _, os := range farmList {
			probe, probed := farmVersion[[2]string{tool, os}]
			stale := probed && now.Sub(probe.at) > CLIFarmReprobeAfter
			if !probed || stale {
				// The probe: learn which version this farm OS has. Seeds are
				// probed unasked; everything else waits to be heard of.
				if !seeded && !seen[tool] {
					continue
				}
				score := int64(0)
				if seeded {
					score = int64(len(domain.CLISeedTools) - seedRank)
				}
				toolGaps = append(toolGaps, WantedRow{
					Ecosystem: "generic", Name: "cli/" + tool, Version: "",
					Symbol: domain.EncodeCLIWorkSymbol(os, ""), Kind: "CLI",
					Axis: AuthoringAxisEvidence, TargetOS: os, Score: score,
				})
				continue
			}
			version := probe.version
			cov.FarmVersions[os] = version
			// Every command the network has seen for this tool, at the
			// version this OS actually has.
			var osGaps []WantedRow
			byCommand := map[string]int{}
			for command, stats := range commands[tool] {
				coord := cliCoord{tool, version, command, os}
				if observed[coord] {
					continue
				}
				score := int64(stats.failRows)*cliFailureWeight + stats.count
				if cliVersionBoundary(stats) {
					score += cliBoundaryWeight
				}
				byCommand[command] = len(osGaps)
				osGaps = append(osGaps, WantedRow{
					Ecosystem: "generic", Name: "cli/" + tool, Version: version,
					Symbol: domain.EncodeCLIWorkSymbol(os, command), Kind: "CLI",
					Axis: AuthoringAxisEvidence, TargetOS: os, Score: score,
				})
			}
			for _, ask := range asks {
				if ask.coord.tool != tool || ask.coord.os != os {
					continue
				}
				if ask.coord.version != version {
					classifyUnavailable(ask.coord, "farm provides another version")
					continue
				}
				if observed[ask.coord] {
					continue
				}
				if i, ok := byCommand[ask.coord.command]; ok {
					osGaps[i].Asks += ask.asks
					osGaps[i].Score += ask.asks * authoringDirectWeight
					continue
				}
				byCommand[ask.coord.command] = len(osGaps)
				osGaps = append(osGaps, WantedRow{
					Ecosystem: "generic", Name: "cli/" + tool, Version: version,
					Symbol: domain.EncodeCLIWorkSymbol(os, ask.coord.command), Kind: "CLI",
					Axis: AuthoringAxisEvidence, TargetOS: os, Asks: ask.asks,
					Score: ask.asks * authoringDirectWeight,
				})
			}
			sortCLIGaps(osGaps)
			if len(osGaps) > CLICommandsPerToolOS {
				osGaps = osGaps[:CLICommandsPerToolOS]
			}
			toolGaps = append(toolGaps, osGaps...)
		}
		for coord, reason := range unavailable {
			if coord.tool == tool {
				cov.Unavailable++
				census.Unavailability[reason]++
			}
		}
		// An ask on an OS the farm has no lane for is unreachable too, even
		// when nothing was ever observed there.
		for _, ask := range asks {
			if ask.coord.tool != tool || farm[ask.coord.os] {
				continue
			}
			if _, done := unavailable[ask.coord]; done {
				continue
			}
			unavailable[ask.coord] = "no farm lane for " + ask.coord.os
			cov.Unavailable++
			census.Unavailability[unavailable[ask.coord]]++
		}
		cov.Queued = len(toolGaps)
		census.Tools = append(census.Tools, cov)
		census.Observed += cov.Observed
		census.FarmObserved += cov.FarmObserved
		census.Unavailable += cov.Unavailable
		gaps = append(gaps, toolGaps...)
	}
	sortCLIGaps(gaps)
	if limit > 0 && len(gaps) > limit {
		gaps = gaps[:limit]
	}
	census.Queued = len(gaps)
	return CLICoveragePlan{Gaps: gaps, Census: census}
}

// cliVersionBoundary reports whether a command's verdict differs between two
// adjacent observed versions: PASS-only at one, FAIL-only at the next.
func cliVersionBoundary(stats *cliCommandStats) bool {
	versions := map[string]bool{}
	for v := range stats.pass {
		versions[v] = true
	}
	for v := range stats.fail {
		versions[v] = true
	}
	if len(versions) < 2 {
		return false
	}
	sorted := make([]string, 0, len(versions))
	for v := range versions {
		sorted = append(sorted, v)
	}
	sort.Slice(sorted, func(i, j int) bool { return domain.CompareVersions(sorted[i], sorted[j]) < 0 })
	verdict := func(v string) string {
		switch {
		case stats.pass[v] && !stats.fail[v]:
			return string(domain.ResultPass)
		case stats.fail[v] && !stats.pass[v]:
			return string(domain.ResultFail)
		}
		return ""
	}
	for i := 0; i+1 < len(sorted); i++ {
		a, b := verdict(sorted[i]), verdict(sorted[i+1])
		if a != "" && b != "" && a != b {
			return true
		}
	}
	return false
}

// sortCLIGaps orders rows by what they are worth, then deterministically.
func sortCLIGaps(rows []WantedRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.TargetOS != b.TargetOS {
			return a.TargetOS < b.TargetOS
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Symbol < b.Symbol
	})
}
