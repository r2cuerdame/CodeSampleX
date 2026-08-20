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
	health := FarmHealth{ReceiptsByOS: map[string]int{}, QuarantinedByReason: map[string]int{}}
	coords := map[string]int{}
	for _, sample := range f.samples {
		if sample.Quarantined {
			health.QuarantinedByReason[sample.QuarantineReason]++
		}
	}
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

func (f *Fake) FarmCoverage(_ context.Context) ([]FarmAxisCoverage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Which (os, purl) pairs the network has seen used.
	observed := map[[2]string]map[string]bool{}
	for agg, meta := range f.aggMeta {
		os := strings.ToLower(authoringEvidenceOS(meta.envJSON))
		pkg, ok := f.packages[agg.PURL]
		if os == "" || !ok || pkg.Publicness != "PUBLIC" {
			continue
		}
		key := [2]string{os, pkg.Ecosystem}
		if observed[key] == nil {
			observed[key] = map[string]bool{}
		}
		observed[key][agg.PURL] = true
	}

	// Which (os, purl) pairs the fleet actually ran there, and which of
	// those passed. A run on one platform says nothing about another, which
	// is the whole reason these are counted apart.
	measured := map[[2]string]map[string]bool{}
	proven := map[[2]string]map[string]bool{}
	for id, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, receipt := range f.receipts[id] {
			result := strings.ToUpper(strings.TrimSpace(receipt.ContractResult))
			if result != "PASS" && result != "FAIL" {
				continue // SKIPPED or unrecorded is not a measurement
			}
			os := farmReceiptOS(receipt.ReceiptJSON)
			if os == "" {
				continue
			}
			// The manifest version is author input and may differ from what a
			// matrix verification actually installed. pg.go settled this for
			// answering a wanted row; coverage must not credit a release the
			// run never resolved.
			credited := resolvedPackageStrings(receipt.ReceiptJSON)
			if len(credited) == 0 {
				credited = manifest.Packages
			}
			for _, purl := range credited {
				pkg, ok := f.packages[purl]
				if !ok || pkg.Publicness != "PUBLIC" {
					continue
				}
				key := [2]string{os, pkg.Ecosystem}
				if measured[key] == nil {
					measured[key] = map[string]bool{}
				}
				measured[key][purl] = true
				if result != "PASS" {
					continue
				}
				if proven[key] == nil {
					proven[key] = map[string]bool{}
				}
				proven[key][purl] = true
			}
		}
	}

	seen := map[[2]string]bool{}
	out := make([]FarmAxisCoverage, 0, len(observed)+len(measured))
	for _, set := range []map[[2]string]map[string]bool{observed, measured} {
		for key := range set {
			if seen[key] {
				continue
			}
			seen[key] = true
			row := FarmAxisCoverage{OS: key[0], Ecosystem: key[1],
				Observed: len(observed[key]),
				Measured: len(measured[key]),
				Proven:   len(proven[key]),
			}
			// The intersection is its own field. Deriving it by filtering
			// Proven -- and falling back to "all of it" when the cell had no
			// observations -- made the number fall when data arrived.
			for purl := range proven[key] {
				if observed[key][purl] {
					row.ObservedProven++
				}
			}
			out = append(out, row)
		}
	}
	// Ecosystem breaks the tie. Without it two cells with equal Observed
	// swap places between calls, and a panel that reorders itself on refresh
	// reads as data changing when nothing did.
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS != out[j].OS {
			return out[i].OS < out[j].OS
		}
		if out[i].Observed != out[j].Observed {
			return out[i].Observed > out[j].Observed
		}
		return out[i].Ecosystem < out[j].Ecosystem
	})
	return out, nil
}

// farmReceiptOS reads the platform a receipt was actually produced on.
// Receipts nest the environment one level deeper than evidence does, and
// reading them with the evidence parser silently yields "" -- which counts as
// unproven everywhere rather than proven somewhere.
func farmReceiptOS(raw string) string {
	var parsed struct {
		Environment struct {
			OS string `json:"os"`
		} `json:"environment"`
	}
	if json.Unmarshal([]byte(raw), &parsed) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Environment.OS))
}
