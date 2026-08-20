package web

import (
	"fmt"
	"sort"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// CoverageRow is one (platform, ecosystem) cell of the network's own
// coverage, counted in distinct public packages.
//
// Observed comes from developer machines; Measured and Proven come from the
// verifier fleet. ObservedProven is the overlap — the only one of the four
// that answers "have we proven what we see used".
type CoverageRow struct {
	OS             string
	Ecosystem      string
	Observed       int
	Measured       int
	Proven         int
	ObservedProven int
}

// PresenceCoverage is one ecosystem's split between packages this network has
// only ever seen INSTALLED and packages it has actually exercised.
type PresenceCoverage struct {
	Ecosystem    string
	PresenceOnly int
	Exercised    int
}

// PresenceGap is one ecosystem's never-exercised share, rendered.
type PresenceGap struct {
	Ecosystem string
	Count     string // "1,167"
	Share     string // "51%"
}

// CoverageDisclosure is the instrument describing its own shape.
//
// An integrity system's failure is "we claimed something false". An
// observatory's is "our coverage was skewed and we never said so", and this
// network is skewed to an extreme that has to be published rather than
// discovered: every observation it holds is from Windows and every proof it
// holds is from Linux, so the overlap between what it sees used and what it
// has proven is zero.
type CoverageDisclosure struct {
	Rows []CoverageRow
	// Overlap is the total ObservedProven across every cell. When it is zero
	// the two halves of the instrument have never met, and the page says so
	// in those words rather than leaving a reader to compute it.
	Overlap int
	// ObservedTotal and ProvenTotal size the two halves being compared.
	ObservedTotal int
	ProvenTotal   int
	// Unreachable names the (platform, ecosystem) pairs that can never be
	// proven, so a zero there is not a backlog. macOS has no container
	// runtime at all and npm publishes no Windows image; a cell that cannot
	// be filled must not be presented as one nobody got to.
	Unreachable []string
	// SelectionNote is true when every recorded verification passed. That
	// looks like a perfect record and is not one: a sample is published
	// because it passed, so the pass rate on our own axis measures our
	// publishing rule and not the ecosystem.
	SelectionNote bool
	// PresenceGaps names the ecosystems whose packages this network has
	// largely only seen installed. It is the larger of the two skews and the
	// one a reader cannot infer: "packages with evidence" reads as packages
	// that were run, and in production 1,167 of npm's 2,289 versions had
	// never been run once.
	PresenceGaps []PresenceGap
}

// buildPresenceGaps renders the never-exercised share per ecosystem, keeping
// only the ecosystems that actually have a gap. A disclosure that cries skew
// everywhere is one nobody reads.
func buildPresenceGaps(lang string, rows []PresenceCoverage) []PresenceGap {
	var out []PresenceGap
	for _, r := range rows {
		total := r.PresenceOnly + r.Exercised
		if r.PresenceOnly == 0 || total == 0 {
			continue
		}
		out = append(out, PresenceGap{
			Ecosystem: r.Ecosystem,
			Count:     i18n.FormatInt(lang, int64(r.PresenceOnly)),
			Share:     fmt.Sprintf("%d%%", int(float64(r.PresenceOnly)/float64(total)*100+0.5)),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ecosystem < out[j].Ecosystem })
	return out
}

func buildCoverageDisclosure(rows []CoverageRow, unreachable []string) CoverageDisclosure {
	out := CoverageDisclosure{Rows: rows, Unreachable: unreachable}
	measured := 0
	for _, r := range rows {
		out.Overlap += r.ObservedProven
		out.ObservedTotal += r.Observed
		out.ProvenTotal += r.Proven
		measured += r.Measured
	}
	out.SelectionNote = measured > 0 && measured == out.ProvenTotal
	sort.SliceStable(out.Rows, func(i, j int) bool {
		if out.Rows[i].OS != out.Rows[j].OS {
			return out.Rows[i].OS < out.Rows[j].OS
		}
		if out.Rows[i].Observed != out.Rows[j].Observed {
			return out.Rows[i].Observed > out.Rows[j].Observed
		}
		return out.Rows[i].Ecosystem < out.Rows[j].Ecosystem
	})
	return out
}

// unreachableCells names the (platform, ecosystem) pairs no verifier can ever
// fill, so a zero there is read as closed rather than as owed.
//
// macOS cannot be containerised at all — there is no Darwin container
// runtime — and npm publishes no Windows base image, so those cells are not
// a backlog anybody can work off. Deriving this from the observed rows rather
// than from a hard-coded list keeps it honest as the corpus grows: a pair
// that never appears is simply not claimed either way.
func unreachableCells(rows []CoverageRow) []string {
	buildable := map[string]bool{"golang": true, "pypi": true}
	seen, out := map[string]bool{}, []string(nil)
	for _, r := range rows {
		if r.OS == "macos" || r.OS == "darwin" {
			if !seen[r.OS+"/"+r.Ecosystem] {
				seen[r.OS+"/"+r.Ecosystem] = true
				out = append(out, r.OS+"/"+r.Ecosystem)
			}
			continue
		}
		if r.OS == "windows" && !buildable[r.Ecosystem] {
			if !seen[r.OS+"/"+r.Ecosystem] {
				seen[r.OS+"/"+r.Ecosystem] = true
				out = append(out, r.OS+"/"+r.Ecosystem)
			}
		}
	}
	sort.Strings(out)
	return out
}
