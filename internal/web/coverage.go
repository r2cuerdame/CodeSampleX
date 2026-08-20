package web

import "sort"

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
