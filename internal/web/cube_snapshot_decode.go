package web

import (
	"context"
	"encoding/json"
)

// Complete current snapshot inputs are stored as (version, symbol, JSON)
// triples. Version and symbol are part of equality: one unchanged document
// must not borrow the coordinates of a different release or API.
func decodeCubeSnapshotDocuments(raw []string) []cubeFact {
	var facts []cubeFact
	for i := 0; i+2 < len(raw); i += 3 {
		var doc snapshotDoc
		if json.Unmarshal([]byte(raw[i+2]), &doc) != nil {
			continue
		}
		for _, row := range doc.Rows {
			if fact, ok := cubeFactFromRow(row, raw[i], raw[i+1]); ok {
				facts = append(facts, fact)
			}
		}
	}
	// A view may append a pinned coordinate. Do not let that append write into
	// the backing array shared by other cached views.
	if cap(facts) != len(facts) {
		exact := make([]cubeFact, len(facts))
		copy(exact, facts)
		facts = exact
	}
	return facts
}

func (c *decodedClusterCache) getCube(ctx context.Context, eco, name string, raw []string) ([]cubeFact, error) {
	result, err := c.load(ctx, "\x00cube-snapshot-documents|"+eco+"|"+name, raw, decodeCubeSnapshots)
	if result == nil {
		return nil, err
	}
	return result.facts, err
}
