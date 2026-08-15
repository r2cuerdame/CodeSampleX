package compatibility

import (
	"fmt"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The cap decides what the network can see: shards are the only document
// clients ever read. Ordering the candidates by hot score and recency meant
// it cut by popularity, and a fresh sample's hot score is zero — so a
// package with more than maxShardSamples samples dropped its STABLE one,
// contract-passed and cross-verified, in favour of twenty newer PUBLISHED
// samples carrying no receipts at all.
//
// Not ranked lower: gone. Every machine on the network then answered that
// package with the unverified twenty and never saw the verified one.
func TestTheBestVerifiedSampleSurvivesTheShardCap(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// The oldest sample is the well-verified one.
	in := []ShardSampleInput{{
		Sample:    ShardSample{SampleID: "sha256:stable", Status: "STABLE"},
		CreatedAt: base,
	}}
	for i := 0; i < maxShardSamples; i++ {
		in = append(in, ShardSampleInput{
			Sample:    ShardSample{SampleID: fmt.Sprintf("sha256:new%02d", i), Status: "PUBLISHED"},
			CreatedAt: base.Add(time.Duration(i+1) * time.Hour),
		})
	}

	got := TopShardSamples(in)
	if len(got) != maxShardSamples {
		t.Fatalf("returned %d samples, want the cap of %d", len(got), maxShardSamples)
	}
	if got[0].SampleID != "sha256:stable" {
		t.Errorf("first sample is %q, want the STABLE one to lead", got[0].SampleID)
	}
}

// A contract-PASS receipt counts even before the status catches up: the
// receipt is the evidence, the status is only the summary of it.
func TestAContractPassOutranksAPlainPublish(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	in := []ShardSampleInput{
		{
			Sample:    ShardSample{SampleID: "sha256:plain", Status: "PUBLISHED"},
			CreatedAt: base.Add(time.Hour), // newer, so recency would win
		},
		{
			Sample: ShardSample{SampleID: "sha256:passed", Status: "PUBLISHED",
				ContractStages: map[string]string{"contract": string(domain.ResultPass)}},
			CreatedAt: base,
		},
	}
	got := TopShardSamples(in)
	if got[0].SampleID != "sha256:passed" {
		t.Errorf("first sample is %q, want the one whose contract actually passed", got[0].SampleID)
	}
}
