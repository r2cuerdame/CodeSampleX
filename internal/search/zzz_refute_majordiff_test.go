package search

import (
	"context"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Probe 1: the reviewer's hand-built shard — a legacy entry with no packages
// field, listed under a shard key of a DIFFERENT major than the request.
func TestZZProbeLegacyShardDifferentMajor(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	saveShardJSON(t, db, "npm/axios/7", shardFile{
		SchemaVersion: 1, Key: "npm/axios/7",
		Packages: []shardPackage{{
			PURL: "pkg:npm/axios@7.0.3",
			Samples: []shardSampleEntry{{
				SampleID: "sha256:shardonly",
				Goal:     "post JSON with axios",
				Status:   "STABLE",
				// no Packages: legacy shard
				Environment: nodeEnv("esm"),
			}},
		}},
	})
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post JSON with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("esm"),
	})
	t.Logf("miss=%v results=%d", resp.Miss, len(resp.Results))
	for _, r := range resp.Results {
		t.Logf("grade=%s score=%.3f exact=%v different=%v", r.Grade, r.Score, r.Exact, r.Different)
	}
}

// Probe 2: the same legacy shard, but the candidate ALSO appears under the
// shard whose key major matches the request — which is what the real builder
// produces, because it lists a sample under every major its manifest declares
// and adds each declared purl as a package entry of that shard.
func TestZZProbeLegacyShardBothMajorsCached(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	entry := shardSampleEntry{
		SampleID: "sha256:shardonly", Goal: "post JSON with axios",
		Status: "STABLE", Environment: nodeEnv("esm"),
	}
	saveShardJSON(t, db, "npm/axios/7", shardFile{
		SchemaVersion: 1, Key: "npm/axios/7",
		Packages: []shardPackage{{PURL: "pkg:npm/axios@7.0.3", Samples: []shardSampleEntry{entry}}},
	})
	saveShardJSON(t, db, "npm/axios/1", shardFile{
		SchemaVersion: 1, Key: "npm/axios/1",
		Packages: []shardPackage{{PURL: "pkg:npm/axios@1.12.0", Samples: []shardSampleEntry{entry}}},
	})
	resp := Engine{DB: db}.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post JSON with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   nodeEnv("esm"),
	})
	t.Logf("miss=%v results=%d", resp.Miss, len(resp.Results))
	for _, r := range resp.Results {
		t.Logf("grade=%s score=%.3f exact=%v different=%v", r.Grade, r.Score, r.Exact, r.Different)
	}
}

// Probe 3: what the reviewer's proposed symmetric cap would do — raise a
// genuine different-major relation to "same package, version unknown".
func TestZZProbeSymmetricCapWouldPromote(t *testing.T) {
	g1, _ := buildGrade(relMajorDiff, nil, contextDelta{}, false)
	g2, _ := buildGrade(relPackageOnly, nil, contextDelta{}, false)
	t.Logf("relMajorDiff -> %s (weight %.2f); relPackageOnly -> %s (weight %.2f)",
		g1, relWeight(relMajorDiff), g2, relWeight(relPackageOnly))
}
