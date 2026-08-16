package compatibility

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
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
	// Believed is what the sample's author says a competent developer or
	// model expects, which the contract below then contradicts.
	//
	// This is the single most useful sentence the network can hand a model,
	// because it names the wrong answer the model was about to write. It
	// costs one line in the shard and saves the round trip that a goal
	// sentence alone forces. Empty on every sample authored before the
	// field existed, and on every sample that corrects nothing.
	Believed string `json:"believed,omitempty"`
	Status   string `json:"status"`
	License  string `json:"license"`
	// Packages is what the sample's manifest declares. It is retained so a
	// client can see the author's input rather than silently replacing it
	// with resolver output.
	Packages []string `json:"packages,omitempty"`
	// Verifications keeps every resolver claim coupled to the environment
	// and stage verdict that established it. Flattening several receipts into
	// one package list would claim combinations that never ran and could
	// attach a PASS from one version to a FAIL at another.
	Verifications  []ShardVerification           `json:"verifications,omitempty"`
	Environment    domain.EnvironmentFingerprint `json:"environment"`
	ContractStages map[string]string             `json:"contractStages,omitempty"`
	// Contract is the assertion list the sample's contract command actually
	// RAN, in a pinned container with the network off, and passed.
	//
	// It is the most useful thing the network knows about a library and it
	// never left the server: the shard carried the sample's one-line goal
	// and nothing else, so every search answer came back with
	// "contract": null and an agent had to spend a second tool call
	// (get_sample) to learn what was actually proven -- which is exactly
	// the per-call, per-option detail a goal sentence cannot carry.
	//
	// Bounded, because a shard is fetched by every client: see
	// contractForShard.
	Contract []string `json:"contract,omitempty"`
}

// ShardVerification is one receipt-scoped resolver claim. Packages, stages
// and environment are deliberately inseparable: together they describe one
// real execution. VerificationLevel is the strongest level proved for the
// same exact package set without exposing peer identities in the shard.
type ShardVerification struct {
	ResolvedPackages  []string                      `json:"resolvedPackages"`
	Environment       domain.EnvironmentFingerprint `json:"environment"`
	Stages            map[string]string             `json:"stages"`
	VerificationLevel int                           `json:"verificationLevel,omitempty"`
	CreatedAt         string                        `json:"createdAt,omitempty"`
}

// Bounds on the contract lines a shard carries. A shard is fetched by every
// client warming that package, so this is paid on the network repeatedly; a
// sample with 40 assertions is also not communicating 40 useful facts.
const (
	maxShardContractLines = 8
	maxShardContractLen   = 240
)

// contractForShard trims a sample's contract to what is worth shipping, and
// says so when it trims: a truncated list that looks complete would be the
// system stating that these are ALL the claims, which is a claim of its own.
func contractForShard(contract []string) []string {
	// Count what a reader would actually have seen, not how long the input
	// slice was: blank and whitespace-only lines are dropped, and counting
	// them made the note claim more was withheld than existed.
	kept := 0
	for _, line := range contract {
		if strings.TrimSpace(line) != "" {
			kept++
		}
	}
	out := make([]string, 0, len(contract))
	for _, line := range contract {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > maxShardContractLen {
			line = strings.TrimSpace(line[:maxShardContractLen]) + "…"
		}
		out = append(out, line)
		if len(out) == maxShardContractLines {
			break
		}
	}
	if len(out) == maxShardContractLines && kept > maxShardContractLines {
		out = append(out, fmt.Sprintf("… and %d more, in the sample itself",
			kept-maxShardContractLines))
	}
	return out
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

// TopShardSamples orders samples for shard inclusion and caps the list.
//
// VERIFICATION FIRST, then hot score, then recency. Ordering by traffic
// alone meant the cap cut by popularity: a package with 21 samples whose
// oldest was STABLE -- contract-passed and cross-verified by independent
// peers -- dropped exactly that one, because twenty newer PUBLISHED
// samples with no receipts at all were more recent and no more popular.
//
// Then COVERAGE. Twenty slots that all answer the same question about the
// same symbol are worth about one slot: a caller asking about a different
// part of the library sees nothing, while the shard is full. So the first
// pass takes the best sample for each distinct symbol before any symbol
// gets a second slot. Density is the point of the cap, not scarcity —
// twenty samples about twenty different things beats twenty about one.
func TopShardSamples(samples []ShardSampleInput) []ShardSample {
	sort.SliceStable(samples, func(i, j int) bool {
		if a, b := verifiedRank(samples[i].Sample), verifiedRank(samples[j].Sample); a != b {
			return a > b
		}
		if samples[i].HotScore != samples[j].HotScore {
			return samples[i].HotScore > samples[j].HotScore
		}
		if !samples[i].CreatedAt.Equal(samples[j].CreatedAt) {
			return samples[i].CreatedAt.After(samples[j].CreatedAt)
		}
		return samples[i].Sample.SampleID < samples[j].Sample.SampleID
	})
	if len(samples) <= maxShardSamples {
		out := make([]ShardSample, 0, len(samples))
		for _, s := range samples {
			out = append(out, s.Sample)
		}
		return out
	}

	// Round one: the best sample for each distinct subject, in rank order.
	picked := make([]ShardSample, 0, maxShardSamples)
	taken := make([]bool, len(samples))
	seen := map[string]bool{}
	for i, s := range samples {
		if len(picked) == maxShardSamples {
			break
		}
		subj := shardSubject(s)
		if seen[subj] {
			continue
		}
		seen[subj] = true
		taken[i] = true
		picked = append(picked, s.Sample)
	}
	// Round two: fill whatever is left, still in rank order.
	for i, s := range samples {
		if len(picked) == maxShardSamples {
			break
		}
		if taken[i] {
			continue
		}
		picked = append(picked, s.Sample)
	}
	return picked
}

// shardSubject is what a sample is ABOUT, for coverage purposes: its first
// declared symbol, else its goal. Two samples sharing it answer the same
// question, however differently they do it.
func shardSubject(s ShardSampleInput) string {
	if len(s.Symbols) > 0 {
		return strings.ToLower(s.Symbols[0])
	}
	return strings.ToLower(s.Sample.Goal)
}

// verifiedRank scores how much of the C13 ladder a sample has actually
// climbed. A contract-PASS receipt counts even when the status has not
// caught up yet: the receipt is the evidence, the status is the summary.
func verifiedRank(s ShardSample) int {
	rank := 0
	switch s.Status {
	case "STABLE":
		rank = 4
	case "MATRIX_PASS":
		rank = 3
	case "CROSS_PASS":
		rank = 2
	case "LOCAL_PASS":
		rank = 1
	}
	if s.ContractStages["contract"] == string(domain.ResultPass) && rank < 1 {
		rank = 1
	}
	for _, v := range s.Verifications {
		if v.Stages["contract"] == string(domain.ResultPass) && rank < 1 {
			rank = 1
		}
	}
	return rank
}

// ShardSampleInput pairs a rendered shard sample with its ordering keys.
type ShardSampleInput struct {
	Sample    ShardSample
	HotScore  float64
	CreatedAt time.Time
	// Symbols is what the sample declares it is about, used to spread the
	// capped list across distinct subjects rather than piling it onto one.
	Symbols []string
}
