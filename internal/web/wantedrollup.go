package web

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// WantedRollupRow is one package version as a reader recognises it, with the
// separate questions asked about it folded in behind.
//
// The stored row is a WORK unit — package, version, symbol, platform — and it
// has to stay that fine-grained, because a Windows report and a Linux report
// about the same release are different questions and a proof of one does not
// answer the other. Rendered raw, though, the board repeated one package four
// times with "1" beside each, and a reader concluded the page was broken
// rather than that four distinct APIs had been asked about.
type WantedRollupRow struct {
	Ecosystem string
	Name      string
	Version   string
	Asks      int64
	// SymbolNames is kept, not just counted: with one distinct API asked
	// about, its name is far more use to a reader than "1 API". The count is
	// what rescues the four-rows-of-the-same-package case, not a replacement
	// for knowing what was asked.
	SymbolNames []string
	Symbols     int
	Platforms   []string
	HasPage     bool
}

// rollUpWanted folds work units into the coordinate a reader recognises.
//
// truncated reports that the window was full, so a package may have rows
// beyond it — an absent row must never read as "nobody asked".
func rollUpWanted(rows []WantedRow, window int) (out []WantedRollupRow, truncated bool) {
	type key struct{ eco, name, version string }
	order := make([]key, 0, len(rows))
	byKey := map[key]*WantedRollupRow{}
	symbols := map[key]map[string]bool{}
	platforms := map[key]map[string]bool{}

	for _, row := range rows {
		k := key{row.Ecosystem, row.Name, row.Version}
		agg, seen := byKey[k]
		if !seen {
			agg = &WantedRollupRow{
				Ecosystem: row.Ecosystem, Name: row.Name,
				Version: row.Version, HasPage: row.HasPage,
			}
			byKey[k] = agg
			order = append(order, k)
			symbols[k] = map[string]bool{}
			platforms[k] = map[string]bool{}
		}
		agg.Asks += row.Asks
		agg.HasPage = agg.HasPage || row.HasPage
		if row.Symbol != "" {
			symbols[k][row.Symbol] = true
		}
		if row.TargetOS != "" {
			platforms[k][row.TargetOS] = true
		}
	}

	out = make([]WantedRollupRow, 0, len(order))
	for _, k := range order {
		agg := byKey[k]
		for name := range symbols[k] {
			agg.SymbolNames = append(agg.SymbolNames, name)
		}
		sort.Strings(agg.SymbolNames)
		agg.Symbols = len(agg.SymbolNames)
		for os := range platforms[k] {
			agg.Platforms = append(agg.Platforms, os)
		}
		sort.Strings(agg.Platforms)
		out = append(out, *agg)
	}
	// Demand is the ranking, and it only becomes meaningful after the fold:
	// before it, every row was a single ask.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Asks != out[j].Asks {
			return out[i].Asks > out[j].Asks
		}
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version > out[j].Version
	})
	return out, window > 0 && len(rows) >= window
}

// wantedRollupWindow bounds the read the fold runs over. The board is a
// ranking of unanswered coordinates, which is a small set by construction —
// a row leaves it the moment a proof arrives — so this is generous, and the
// page says so when it fills.
const wantedRollupWindow = 2000

// wantedDetail names what the fold covered, so a reader can tell a whole
// package question from four separate API questions that happen to share a
// release.
func wantedDetail(lang string, row WantedRollupRow) string {
	parts := make([]string, 0, 2)
	switch {
	case row.Symbols == 1:
		parts = append(parts, row.SymbolNames[0])
	case row.Symbols > 1:
		parts = append(parts, i18n.T(lang, "wanted.detail_symbols", i18n.FormatInt(lang, int64(row.Symbols))))
	}
	if len(row.Platforms) > 0 {
		parts = append(parts, i18n.T(lang, "wanted.detail_platforms", strings.Join(row.Platforms, ", ")))
	}
	return strings.Join(parts, " · ")
}
