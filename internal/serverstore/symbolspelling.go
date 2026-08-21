package serverstore

import (
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// symbolSpellings returns every name one symbol is filed under.
//
// Snapshot targets arrive from two places that do not agree. evidence_agg
// carries the name the SCANNER wrote, which qualifies a symbol with its
// package — "github.com/google/uuid.New", "semver.coerce". A sample manifest
// carries the name the AUTHOR wrote, which is usually bare — "MarshalText".
// Both become coordinates, so one symbol exists under two keys and a lookup
// by the author's spelling matches none of the scanner's rows.
//
// Measured in production: of 1,235 golang symbol coordinates only 287 found
// any observation, while every golang row in evidence_agg was qualified and
// 630 of the coordinates were bare. Those coordinates were not saying "no
// observations". They were saying nobody had reconciled two spellings, and a
// reader cannot tell those apart.
//
// Reconciling on the way IN would move the keys, and the keys are in URLs
// people hold. So it is done on the way out, here, where both stores share
// one answer — a Fake and a PG that disagree about this would be a silent
// production bug.
func symbolSpellings(purl, symbol string) []string {
	if symbol == "" {
		return []string{""} // the package-level row has nothing to qualify
	}
	p, err := domain.ParsePURL(purl)
	if err != nil || p.Name == "" {
		return []string{symbol}
	}
	qualified := p.Name + "." + symbol
	if symbol == qualified || strings.HasPrefix(symbol, p.Name+".") {
		return []string{symbol}
	}
	return []string{symbol, qualified}
}
