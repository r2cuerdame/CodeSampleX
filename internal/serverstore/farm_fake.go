package serverstore

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

func (f *Fake) FarmWorkers(_ context.Context, since, now time.Time) ([]FarmWorker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FarmWorker, 0, len(f.authoring))
	for _, row := range f.authoring {
		if !row.RevokedAt.IsZero() || !now.Before(row.IdleExpiresAt) {
			continue
		}
		w := FarmWorker{
			Label: row.Label, ComputerName: row.ComputerName, IssuedAt: row.IssuedAt,
			LastRefreshAt: row.LastRefreshAt, IdleExpiresAt: row.IdleExpiresAt,
		}
		for _, draft := range f.authoringDrafts {
			if draft.SessionID != row.SessionID || draft.CreatedAt.Before(since) {
				continue
			}
			w.Drafts++
			if sample, ok := f.samples[draft.SampleID]; ok && !sample.Quarantined {
				w.Published++
			}
		}
		for _, work := range f.authoringWork {
			if work.SessionID == row.SessionID && work.SampleID == "" {
				symbol := work.Symbol
				if symbol == "" {
					symbol = "(package)"
				}
				w.Holding = work.Name + "@" + work.Version + " " + symbol
				break
			}
		}
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out, nil
}

func (f *Fake) FarmHealthNow(_ context.Context, now time.Time) (FarmHealth, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	health := FarmHealth{ReceiptsByOS: map[string]int{}}
	coords := map[string]int{}
	for id, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		health.PublicSamples++
		var manifest struct {
			Packages []string `json:"packages"`
			Symbols  []string `json:"symbols"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) == nil && len(manifest.Packages) > 0 {
			symbols := append([]string(nil), manifest.Symbols...)
			sort.Strings(symbols)
			coords[manifest.Packages[0]+"\x00"+strings.Join(symbols, ",")]++
		}
		for _, receipt := range f.receipts[id] {
			if receipt.ContractResult != "PASS" {
				continue
			}
			var parsed struct {
				Environment struct {
					OS string `json:"os"`
				} `json:"environment"`
			}
			if json.Unmarshal([]byte(receipt.ReceiptJSON), &parsed) == nil && parsed.Environment.OS != "" {
				health.ReceiptsByOS[strings.ToLower(parsed.Environment.OS)]++
			}
		}
	}
	for _, n := range coords {
		if n > 1 {
			health.DuplicateCoords++
		}
	}
	live := map[string]bool{}
	for _, row := range f.authoring {
		live[row.SessionID] = row.RevokedAt.IsZero() && now.Before(row.IdleExpiresAt)
	}
	for _, work := range f.authoringWork {
		if work.SampleID == "" && !live[work.SessionID] {
			health.StaleClaims++
		}
	}
	return health, nil
}

var _ FarmStatsStore = (*Fake)(nil)
