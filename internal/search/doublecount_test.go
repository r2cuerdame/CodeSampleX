package search

import (
	"testing"
)

// A shard lists a sample under every package version it is relevant to, and
// the evidence map is keyed by ecosystem/name with no version in it. So a
// candidate declaring axios@1.12.0 and axios@1.13.0 looked the SAME entry up
// twice and collected its symbols twice — and every observation count built
// from them came out doubled, in exactly the numbers a caller reads as
// measurements of how much the network has seen.
func TestEvidenceIsNotCountedOncePerVersion(t *testing.T) {
	entry := &pkgEvidence{symbols: []shardSymbolEntry{
		{Family: "axios.post"},
	}}
	evidence := map[string]*pkgEvidence{"npm/axios": entry}

	one := &candidate{packages: parsePURLs([]string{"pkg:npm/axios@1.12.0"})}
	two := &candidate{packages: parsePURLs([]string{"pkg:npm/axios@1.12.0", "pkg:npm/axios@1.13.0"})}

	count := func(c *candidate) int {
		seen := map[string]bool{}
		n := 0
		for _, p := range c.packages {
			k := pkgKey(p)
			if seen[k] {
				continue
			}
			seen[k] = true
			if pe := evidence[k]; pe != nil {
				n += len(pe.symbols)
			}
		}
		return n
	}
	if a, b := count(one), count(two); a != b {
		t.Errorf("one version collected %d symbol entries, two versions collected %d — "+
			"the same evidence counted twice", a, b)
	}
}
