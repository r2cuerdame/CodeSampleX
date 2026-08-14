package compatibility

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// maxShardSamples caps how many top samples ride inside one shard.
const maxShardSamples = 20

// ShardSymbolStats is the compact per-symbol statistics block of contract C6.
type ShardSymbolStats struct {
	ObservationCount  int64                 `json:"observationCount"`
	UniquePeerBuckets int                   `json:"uniquePeerBuckets"`
	PassRate          float64               `json:"passRate"`
	ByStage           map[string]StageCount `json:"byStage"`
	Confidence        string                `json:"confidence"`
	LastSeen          string                `json:"lastSeen,omitempty"`
}

// ShardFailure is one failure signature inside a shard symbol entry.
type ShardFailure struct {
	ErrorCode   string            `json:"errorCode,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Count       int64             `json:"count"`
	EnvSummary  map[string]string `json:"envSummary,omitempty"`
}

// ShardSymbol is one symbol family entry of a shard package.
type ShardSymbol struct {
	Family   string           `json:"family"`
	Stats    ShardSymbolStats `json:"stats"`
	Failures []ShardFailure   `json:"failures,omitempty"`
}

// ShardSample is one top sample carried in a shard, including its honest
// verification status and the contract stages its receipts actually showed.
type ShardSample struct {
	SampleID string `json:"sampleId"`
	Goal     string `json:"goal,omitempty"`
	Status   string `json:"status"`
	License  string `json:"license"`
	// Packages is what the sample's manifest declares. A shard lists one
	// sample under every package version it is relevant to, so without this
	// a client has no way to tell which of those it was actually verified
	// against — and the local engine was grading against the shard key,
	// reporting an exact match on a version the sample never used.
	Packages       []string                      `json:"packages,omitempty"`
	Environment    domain.EnvironmentFingerprint `json:"environment"`
	ContractStages map[string]string             `json:"contractStages,omitempty"`
}

// ShardPackage is one package version inside a shard.
type ShardPackage struct {
	PURL    string        `json:"purl"`
	Symbols []ShardSymbol `json:"symbols,omitempty"`
	Samples []ShardSample `json:"samples,omitempty"`
}

// Shard is the C6 wire document for one (ecosystem, package, major).
type Shard struct {
	SchemaVersion int            `json:"schemaVersion"`
	Key           string         `json:"key"`
	GeneratedAt   string         `json:"generatedAt"`
	Packages      []ShardPackage `json:"packages"`
}

// BuildShard renders the canonical shard JSON and its ETag. The ETag is the
// sha256 hex of the canonical JSON, so identical inputs (including
// generatedAt) always produce the identical etag.
func BuildShard(key string, packages []ShardPackage, generatedAt time.Time) (shardJSON, etag string) {
	shard := Shard{
		SchemaVersion: 1,
		Key:           key,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339),
		Packages:      packages,
	}
	js := domain.MustCanonicalJSON(shard)
	sum := sha256.Sum256(js)
	return string(js), hex.EncodeToString(sum[:])
}

// SymbolStatsFromEvidence condenses one symbol's evidence rows into the C6
// stats block plus its failure list.
func SymbolStatsFromEvidence(rows []serverstore.EvidenceRow, now time.Time) (ShardSymbolStats, []ShardFailure) {
	stats := ShardSymbolStats{ByStage: map[string]StageCount{}}
	var samples []Sample
	var lastSeen time.Time
	maxPeers := 0
	for _, row := range rows {
		if !isObservationStage(row.Stage) {
			continue
		}
		sc := stats.ByStage[row.Stage]
		if row.Result == string(domain.ResultPass) {
			sc.Pass += row.ObservationCount
		} else {
			sc.Fail += row.ObservationCount
		}
		stats.ByStage[row.Stage] = sc
		stats.ObservationCount += row.ObservationCount
		samples = append(samples, Sample{
			Class:  domain.ClassUsageObservation,
			Result: domain.Result(row.Result),
			Count:  row.ObservationCount,
			Age:    now.Sub(row.LastSeen),
		})
		if row.UniquePeerBuckets > maxPeers {
			maxPeers = row.UniquePeerBuckets
		}
		if row.LastSeen.After(lastSeen) {
			lastSeen = row.LastSeen
		}
	}
	v := Compute(samples, int64(maxPeers))
	stats.UniquePeerBuckets = maxPeers
	stats.PassRate = v.PassRate
	stats.Confidence = v.Confidence
	if !lastSeen.IsZero() {
		stats.LastSeen = lastSeen.UTC().Format(time.RFC3339)
	}

	var failures []ShardFailure
	for _, f := range failureSummaries(rows) {
		failures = append(failures, ShardFailure{
			ErrorCode:   f.ErrorCode,
			Fingerprint: f.Fingerprint,
			Count:       f.Count,
			EnvSummary:  f.EnvSummary,
		})
	}
	return stats, failures
}

// TopShardSamples orders samples by hot score, then recency, and caps the
// list for shard inclusion.
func TopShardSamples(samples []ShardSampleInput) []ShardSample {
	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].HotScore != samples[j].HotScore {
			return samples[i].HotScore > samples[j].HotScore
		}
		if !samples[i].CreatedAt.Equal(samples[j].CreatedAt) {
			return samples[i].CreatedAt.After(samples[j].CreatedAt)
		}
		return samples[i].Sample.SampleID < samples[j].Sample.SampleID
	})
	if len(samples) > maxShardSamples {
		samples = samples[:maxShardSamples]
	}
	out := make([]ShardSample, 0, len(samples))
	for _, s := range samples {
		out = append(out, s.Sample)
	}
	return out
}

// ShardSampleInput pairs a rendered shard sample with its ordering keys.
type ShardSampleInput struct {
	Sample    ShardSample
	HotScore  float64
	CreatedAt time.Time
}
