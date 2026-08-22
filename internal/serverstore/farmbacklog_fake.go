package serverstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// provenCoordinates is what the network has actually proven: the purls a
// non-quarantined sample passed a contract for, and for each of them the
// platforms those passes ran on. The caller holds f.mu.
//
// It is one pass shared by the scheduler and the operations panel rather than
// two, because "proven" is exactly the definition the backlog and the queue
// have to agree on: a panel that counted a different set from the queue it
// describes would report a number that never reaches zero, or one that reaches
// zero while work is still going out.
func (f *Fake) provenCoordinates() (verified map[string]bool, targets map[string]map[string]bool) {
	verified = make(map[string]bool)
	targets = make(map[string]map[string]bool)
	for sampleID, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, receipt := range f.receipts[sampleID] {
			if receipt.ContractResult != "PASS" {
				continue
			}
			for _, purl := range manifest.Packages {
				verified[purl] = true
			}
			var parsed struct {
				Environment struct {
					OS string `json:"os"`
				} `json:"environment"`
			}
			if json.Unmarshal([]byte(receipt.ReceiptJSON), &parsed) != nil || parsed.Environment.OS == "" {
				continue
			}
			for _, purl := range manifest.Packages {
				if targets[purl] == nil {
					targets[purl] = make(map[string]bool)
				}
				targets[purl][strings.ToLower(parsed.Environment.OS)] = true
			}
		}
	}
	return verified, targets
}

// provenNameTargets lifts provenCoordinates from releases to package names:
// which packages the network proves at SOME version, and where. Caller holds
// f.mu.
func (f *Fake) provenNameTargets(packageTargets map[string]map[string]bool) map[[2]string]map[string]bool {
	nameTargets := make(map[[2]string]map[string]bool)
	for purl, oses := range packageTargets {
		proven, ok := f.packages[purl]
		if !ok {
			continue
		}
		key := [2]string{proven.Ecosystem, proven.Name}
		if nameTargets[key] == nil {
			nameTargets[key] = make(map[string]bool)
		}
		for targetOS := range oses {
			nameTargets[key][targetOS] = true
		}
	}
	return nameTargets
}

// sightedCoordinates splits evidence two ways: every purl it names, and the
// subset somebody listed in their own manifest. Caller holds f.mu.
func (f *Fake) sightedCoordinates() (observed, chosen map[string]bool) {
	observed = make(map[string]bool)
	chosen = make(map[string]bool)
	for key := range f.merge.observations {
		observed[key.PURL] = true
		if meta := f.aggMeta[key]; meta != nil && meta.direct {
			chosen[key.PURL] = true
		}
	}
	return observed, chosen
}

func (f *Fake) FarmBacklogNow(_ context.Context, since, now time.Time) (FarmBacklog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	backlog := FarmBacklog{ClaimedByKind: map[string]int{}}

	observed, chosen := f.sightedCoordinates()
	verified, packageTargets := f.provenCoordinates()

	// A `-` cell: the network watches people use this exact release and has
	// never proven it. Restricted to PUBLIC rows because a coordinate the
	// queue may not offer is not a backlog anybody can work off.
	for purl, pkg := range f.packages {
		if pkg.Version == "" || pkg.Publicness != "PUBLIC" {
			continue
		}
		if observed[purl] && !verified[purl] {
			backlog.CoverageHoles++
		}
	}
	backlog.Dependencies = len(f.dependencyOpen(observed, chosen, verified,
		f.provenNameTargets(packageTargets)))

	for _, work := range f.authoringWork {
		if work.ClaimedAt.Before(since) || work.ClaimedAt.After(now) {
			continue
		}
		kind := work.Kind
		if kind == "" {
			kind = "WANTED"
		}
		backlog.ClaimedByKind[kind]++
	}

	// The first pass, not any pass. Re-proving a coordinate on another
	// platform is real work, but it takes nothing off the backlog above, and
	// a flow that does not drain the stock it is printed beside is a number
	// an operator cannot act on.
	firstPass := map[string]time.Time{}
	for sampleID, sample := range f.samples {
		if sample.Quarantined {
			continue
		}
		var manifest struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, receipt := range f.receipts[sampleID] {
			if receipt.ContractResult != "PASS" || receipt.CreatedAt.IsZero() {
				continue
			}
			for _, purl := range manifest.Packages {
				if at, ok := firstPass[purl]; !ok || receipt.CreatedAt.Before(at) {
					firstPass[purl] = receipt.CreatedAt
				}
			}
		}
	}
	for _, at := range firstPass {
		if !at.Before(since) && !at.After(now) {
			backlog.FirstProven++
		}
	}
	return backlog, nil
}
