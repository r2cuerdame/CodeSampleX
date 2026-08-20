package serverstore

import "sort"

// receiptClaim is one verified receipt's contribution to the snapshot target
// list: the packages it resolved, the symbols its sample declared, and the
// coordinate the sample says it is ABOUT when it says so.
type receiptClaim struct {
	Packages []string
	Symbols  []string
	// Subject is the purl the sample was written for, "" when the manifest
	// predates the field. The authoring queue assigns an exact coordinate, so
	// when it is present nothing has to be inferred.
	Subject string
}

// snapshotTargetsFromClaims turns verified receipts into (purl, symbol)
// targets.
//
// The rule used to be the cartesian product: every package a receipt resolved
// against every symbol its sample declared. A receipt resolves the whole
// lockfile, so a Sinatra sample tested with minitest filed Sinatra::Base
// under minitest, rack, rack-test, mustermann and rack-protection too. The
// front page, asked for minitest, answered with Faraday's API — and in
// production 680 of 3,881 symbols were claimed by more than one package, one
// of them by 21.
//
// A symbol belongs to one package. Two things decide which:
//
//   - A stated Subject wins outright. The queue assigned that coordinate.
//   - Otherwise the NARROWEST claim wins: among the receipts declaring a
//     symbol, the one that resolved the fewest packages is the one that says
//     most about where the symbol lives. A sample written for faraday alone
//     beats a suite that dragged in five gems to exercise it.
//
// A symbol nothing else claims keeps every package of its one sample. That
// is today's behaviour, and it is the floor: this narrows attribution, it
// does not delete coverage.
//
// Package-level targets are untouched. Every package a receipt resolved
// really was present, and the symbol-"" snapshot is what records that.
func snapshotTargetsFromClaims(claims []receiptClaim) []SnapshotTarget {
	// A stated subject is narrower than any resolved set can be. It cannot
	// share the sentinel with "resolved nothing": a v1 receipt establishes no
	// version and so carries no packages, and treating that as a subject
	// filed every symbol under the empty purl.
	const subjectWidth = -1

	width := func(c receiptClaim) int {
		if c.Subject != "" {
			return subjectWidth
		}
		return len(c.Packages)
	}
	// A claim that resolved nothing and states no subject cannot own a symbol,
	// so it must not enter the narrowness contest: a v1 receipt establishes no
	// version, and letting it win meant the symbol was awarded to a claim with
	// no package to award it to and disappeared from the one that had it.
	owns := func(c receiptClaim) bool { return c.Subject != "" || len(c.Packages) > 0 }

	narrowest := map[string]int{}
	for _, c := range claims {
		if !owns(c) {
			continue
		}
		w := width(c)
		for _, symbol := range c.Symbols {
			if symbol == "" {
				continue
			}
			if best, ok := narrowest[symbol]; !ok || w < best {
				narrowest[symbol] = w
			}
		}
	}

	seen := map[SnapshotTarget]bool{}
	for _, c := range claims {
		w := width(c)
		for _, purl := range c.Packages {
			seen[SnapshotTarget{PURL: purl}] = true
		}
		if !owns(c) {
			continue
		}
		if w >= 0 {
			for _, symbol := range c.Symbols {
				if symbol == "" || narrowest[symbol] != w {
					continue
				}
				for _, purl := range c.Packages {
					seen[SnapshotTarget{PURL: purl, Symbol: symbol}] = true
				}
			}
			continue
		}
		// A stated subject takes its symbols and nothing else does.
		for _, symbol := range c.Symbols {
			if symbol != "" {
				seen[SnapshotTarget{PURL: c.Subject, Symbol: symbol}] = true
			}
		}
	}

	out := make([]SnapshotTarget, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out
}
