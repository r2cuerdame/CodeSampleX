package serverstore

import (
	"sort"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// VersionCoresidence is one pair of versions of the same library that a
// scanner saw in a SINGLE resolution.
//
// The server cannot work this out for itself: an observation batch carries a
// single package, so a lockfile arrives already shredded into independent
// records and the finest grouping left is a project and a day. A project that
// builds twice in an afternoon against different lockfiles produces exactly
// the input that would be read as a collision, so pairing here would be
// inference. The scanner holds the lockfile at once and reports the pair.
//
// Projects counts distinct project-days, not rebuilds. Failing is the subset
// where a build failed with a cause someone could name — an unattributed
// failure says a build containing this package broke and nothing about which
// package broke it.
type VersionCoresidence struct {
	Lower    string
	Higher   string
	Projects int
	Failing  int
}

// coresidencePairs turns one batch into the pairs it witnesses.
//
// A pair is unordered, so it is stored with the lower version first: 7.5.0
// beside 8.19.0 is one fact whichever package reported it, and the two
// packages of a collision each report the other.
func coresidencePairs(b domain.ObservationBatch) []VersionCoresidence {
	p, err := domain.ParsePURL(b.Package)
	if err != nil || p.Version == "" || b.Symbol != "" {
		return nil
	}
	seen := map[VersionCoresidence]bool{}
	for _, other := range b.Coresident {
		other = strings.TrimSpace(other)
		if other == "" || other == p.Version {
			continue
		}
		lo, hi := orderVersionPair(p.Version, other)
		seen[VersionCoresidence{Lower: lo, Higher: hi}] = true
	}
	out := make([]VersionCoresidence, 0, len(seen))
	for pair := range seen {
		out = append(out, pair)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lower != out[j].Lower {
			return out[i].Lower < out[j].Lower
		}
		return out[i].Higher < out[j].Higher
	})
	return out
}

// batchNamesAnAttributedFailure reports whether this batch is a failure
// anyone could name a cause for.
func batchNamesAnAttributedFailure(b domain.ObservationBatch) bool {
	return b.Result == domain.ResultFail && strings.TrimSpace(b.ErrorCode) != ""
}

// orderVersionPair puts the pair in VERSION precedence, which is what
// "Lower" and "Higher" claim. Plain string order put "10.0.0" below
// "9.0.0"; equal-precedence spellings fall back to string order so the
// canonical identity stays deterministic.
func orderVersionPair(a, b string) (lo, hi string) {
	c := domain.CompareVersions(a, b)
	if c > 0 || (c == 0 && a > b) {
		return b, a
	}
	return a, b
}

// canonicalCoresidencePairs heals rows stored under the old lexicographic
// rule at read time: each pair is re-ordered to precedence, and a pair that
// exists in both spellings is folded into one so it never renders twice. The
// display order (most failing, then most seen) is re-applied after the fold.
func canonicalCoresidencePairs(rows []VersionCoresidence) []VersionCoresidence {
	merged := map[[2]string]VersionCoresidence{}
	for _, r := range rows {
		lo, hi := orderVersionPair(r.Lower, r.Higher)
		k := [2]string{lo, hi}
		m := merged[k]
		m.Lower, m.Higher = lo, hi
		m.Projects += r.Projects
		m.Failing += r.Failing
		merged[k] = m
	}
	out := make([]VersionCoresidence, 0, len(merged))
	for _, m := range merged {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failing != out[j].Failing {
			return out[i].Failing > out[j].Failing
		}
		if out[i].Projects != out[j].Projects {
			return out[i].Projects > out[j].Projects
		}
		if out[i].Lower != out[j].Lower {
			return out[i].Lower < out[j].Lower
		}
		return out[i].Higher < out[j].Higher
	})
	return out
}
