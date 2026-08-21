package domain

import (
	"sort"
	"strings"
)

// DedupeResultsByCoordinate keeps one result per (packages, symbols)
// coordinate.
//
// Two samples for the same coordinate are the same answer twice. Side by side
// they burn the caller's result budget and, worse, read as corroboration --
// two independent sources agreeing -- when they are one coordinate measured
// twice by the same fleet. The corpus reached 37% duplicates before the work
// queue stopped issuing the same coordinate repeatedly, and the ones already
// published do not disappear on their own.
//
// This hides rather than withdraws. A duplicate is still evidence and still
// answers its own sample page; quarantine is for a takedown, not for
// tidiness, and using it here would delete work the network paid for.
//
// It lives in domain because BOTH search surfaces apply it: the server's HTTP
// handler and the local engine behind search_known_solution. Folding one and
// not the other left the primary agent surface returning the same coordinate
// twice in its three-result window.
//
// The input is expected in descending score order, so the first result for a
// coordinate is the best one; the ordering is preserved otherwise.
func DedupeResultsByCoordinate(results []SearchResult) []SearchResult {
	if len(results) < 2 {
		return results
	}
	seen := make(map[string]bool, len(results))
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		key, ok := ResultCoordinate(r)
		// A result that declares no coordinate cannot be compared to
		// anything. Folding those together would silently drop an answer on
		// the strength of a key that says nothing.
		if !ok {
			out = append(out, r)
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

// ResultCoordinate is the canonical (packages, symbols) key a duplicate
// shares. ok is false when the result declares neither, which is not a
// coordinate anyone can collide on.
func ResultCoordinate(r SearchResult) (string, bool) {
	if r.Case == nil {
		return "", false
	}
	packages := append([]string(nil), r.Case.Packages...)
	symbols := append([]string(nil), r.Case.Symbols...)
	if len(packages) == 0 && len(symbols) == 0 {
		return "", false
	}
	sort.Strings(packages)
	sort.Strings(symbols)
	return strings.Join(packages, ",") + "\x00" + strings.Join(symbols, ","), true
}
