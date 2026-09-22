package serverstore

import (
	"context"
	"sort"
	"strings"
)

// ListCLIObservations is the Fake half of CLIObservationStore: every
// aggregate row whose purl names a public CLI target, with the OS read from
// the stored environment the same way the PostgreSQL query reads env_json.
func (f *Fake) ListCLIObservations(_ context.Context, limit int) ([]CLIObservationRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []CLIObservationRow
	for key, count := range f.merge.observations {
		if !strings.HasPrefix(key.PURL, "pkg:generic/cli/") {
			continue
		}
		row := CLIObservationRow{PURL: key.PURL, Symbol: key.Symbol, Result: key.Result, Count: count}
		if meta := f.aggMeta[key]; meta != nil {
			row.OS = authoringEvidenceOS(meta.envJSON)
			row.LastSeen = meta.lastSeen
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].OS < out[j].OS
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
