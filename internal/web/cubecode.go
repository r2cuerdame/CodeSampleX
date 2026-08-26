package web

// ---------------------------------------------------------------------------
// Code availability, kept apart from environment evidence.
//
// The grid used to answer both questions with one mark. "≡" was documented as
// "our own sample ran here and came back clean" AND as "there is code that
// works for this cell", which are not the same fact and do not have the same
// key: a sample belongs to a RELEASE and an API, and it does not stop existing
// because the reader switched the OS filter to a platform the fleet never ran
// it on. Readers took the mark for "there is code here, go deeper", followed
// it down to a coordinate that had no code to open, and were right to be
// annoyed — the mark had promised something the coordinate never held.
//
// So the two facts are separated at their keys:
//
//	code availability   package + version + symbol/API      (environment-free)
//	compatibility       … + OS + libc + arch + runtime …    (per environment)
//
// codeIndex is the first of those. It is built from the store's exhaustive
// verified-sample aggregate, not the bounded newest-N list rendered below the
// page, and it never reads an environment dimension — not as a filter, not as
// a tiebreak — because the moment it does, a Windows reader is told there is
// no code for something that has ninety-six samples.

// codeKey addresses one release's answers for one API. The member, not the
// full spelling: a sample filed against "uuid.New" and a symbol axis labelled
// "New" are the same API, and the site already matches them that way
// (sampleNamesSymbol).
type codeKey struct{ version, member string }

// codeIndex counts published samples per release and per (release, API).
//
// Counts rather than booleans: "코드 있음" is the fact, but a reader deciding
// whether to open the list wants to know whether it holds one answer or forty,
// and the count costs nothing to carry. They are int64 because that is what
// the page's number formatter takes — an int reaches it as a type mismatch and
// html/template aborts the render mid-tag rather than saying so.
type codeIndex struct {
	byVersion map[string]int64
	bySymbol  map[codeKey]int64
	total     int64
	// known distinguishes an authoritative empty aggregate from a failed
	// store read. Without it a transient database error becomes the public
	// claim "No code or sample yet".
	known bool
}

func newCodeIndex(items []SampleListItem) *codeIndex {
	c := &codeIndex{
		byVersion: map[string]int64{},
		bySymbol:  map[codeKey]int64{},
		known:     true,
	}
	for _, it := range items {
		c.total++
		if it.Version != "" {
			c.byVersion[it.Version]++
		}
		for _, sym := range it.Symbols {
			m := symbolMember(sym)
			if m == "" {
				continue
			}
			c.bySymbol[codeKey{it.Version, m}]++
		}
	}
	return c
}

func newCodeIndexFromCounts(rows []PackageCodeCount) *codeIndex {
	c := &codeIndex{
		byVersion: map[string]int64{},
		bySymbol:  map[codeKey]int64{},
		known:     true,
	}
	for _, row := range rows {
		if row.Version == "" || row.Samples <= 0 {
			continue
		}
		if row.Symbol == "" {
			c.byVersion[row.Version] += row.Samples
			c.total += row.Samples
			continue
		}
		member := symbolMember(row.Symbol)
		if member != "" {
			c.bySymbol[codeKey{row.Version, member}] += row.Samples
		}
	}
	return c
}

func unknownCodeIndex() *codeIndex {
	return &codeIndex{byVersion: map[string]int64{}, bySymbol: map[codeKey]int64{}}
}

// at reports how many published samples answer this release and this API.
//
// The empty symbol and cubePackageLevel both mean "the release as a whole":
// package-level evidence is not a roll-up of the symbols beside it, but a
// reader standing on the package-level row is asking about the release, and
// the release's own answers are what it has.
func (c *codeIndex) at(version, symbol string) int64 {
	if c == nil {
		return 0
	}
	if symbol == "" || symbol == cubePackageLevel {
		if version == "" {
			return c.total
		}
		return c.byVersion[version]
	}
	member := symbolMember(symbol)
	if member == "" {
		return 0
	}
	if version != "" {
		return c.bySymbol[codeKey{version, member}]
	}
	// No release decided yet: the API has code somewhere in the window, and
	// saying so is more use than saying nothing until the reader picks one.
	var n int64
	for k, v := range c.bySymbol {
		if k.member == member {
			n += v
		}
	}
	return n
}
