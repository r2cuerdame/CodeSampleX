package compatibility

import (
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// SnapshotRow is one environment-bucket row of a compatibility snapshot.
// The execution-context label leads (docs/execution-context.md §6);
// observation class counts and verification counts are kept in separate
// maps and are NEVER summed together (goal.md §3.5).
type SnapshotRow struct {
	ContextLabel string                        `json:"contextLabel"`
	EnvBucket    domain.EnvironmentFingerprint `json:"envBucket"`
	// ByStage tallies per stage. Observation stages (PROJECT_*, USED) come
	// from anonymous evidence; the CONTRACT key comes from signed receipts.
	// The stage key is the separation — no cell mixes the two.
	ByStage map[string]StageCount `json:"byStage"`
	// ObservationClassCounts counts weak co-occurrence evidence by class.
	ObservationClassCounts map[string]int64 `json:"observationClassCounts"`
	// VerificationCounts counts strong receipt-backed evidence by class.
	VerificationCounts map[string]int64 `json:"verificationCounts"`
	PassRate           float64          `json:"passRate"`
	Confidence         string           `json:"confidence"`
	ElevatedFailure    bool             `json:"elevatedFailure"`
	UniquePeerBuckets  int              `json:"uniquePeerBuckets"`
	LastSeen           string           `json:"lastSeen,omitempty"`
}

// FailureSummary is one failure signature aggregated inside a snapshot.
type FailureSummary struct {
	Stage       string            `json:"stage"`
	ErrorCode   string            `json:"errorCode,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Count       int64             `json:"count"`
	EnvSummary  map[string]string `json:"envSummary,omitempty"`
}

// Snapshot is the §7.8 read-optimized compatibility document stored per
// (purl, symbol) and served by registry/web reads — requests never
// re-aggregate raw evidence.
type Snapshot struct {
	SchemaVersion        int                   `json:"schemaVersion"`
	PURL                 string                `json:"purl"`
	Symbol               string                `json:"symbol"`
	Rows                 []SnapshotRow         `json:"rows"`
	Failures             []FailureSummary      `json:"failures"`
	RegressionCandidates []RegressionCandidate `json:"regressionCandidates,omitempty"`
	GeneratedAt          string                `json:"generatedAt"`
}

// BuildSnapshot computes the snapshot for one (purl, symbol) target from its
// aggregated evidence rows and the receipts of samples covering the target.
// Pure: no I/O, unit-testable without a database.
func BuildSnapshot(purl, symbol string, evidence []serverstore.EvidenceRow,
	receipts []ReceiptInfo, regressions []RegressionCandidate, now time.Time) Snapshot {

	type group struct {
		bucket   domain.EnvironmentFingerprint
		byStage  map[string]StageCount
		obsCount int64
		samples  []Sample
		maxPeers int
		recPeers map[string]bool
		verCount int64
		lastSeen time.Time
	}
	groups := map[bucketKey]*group{}
	get := func(env domain.EnvironmentFingerprint) (bucketKey, *group) {
		key, bucket := bucketOf(env)
		g := groups[key]
		if g == nil {
			g = &group{bucket: bucket, byStage: map[string]StageCount{}, recPeers: map[string]bool{}}
			groups[key] = g
		}
		return key, g
	}

	for _, row := range evidence {
		env, ok := parseEnv(row.EnvJSON)
		if !ok || !isObservationStage(row.Stage) {
			continue
		}
		_, g := get(env)
		sc := g.byStage[row.Stage]
		if row.Result == string(domain.ResultPass) {
			sc.Pass += row.ObservationCount
		} else {
			sc.Fail += row.ObservationCount
		}
		g.byStage[row.Stage] = sc
		g.obsCount += row.ObservationCount
		g.samples = append(g.samples, Sample{
			Class:  domain.ClassUsageObservation,
			Result: domain.Result(row.Result),
			Count:  row.ObservationCount,
			Age:    now.Sub(row.LastSeen),
		})
		if row.UniquePeerBuckets > g.maxPeers {
			g.maxPeers = row.UniquePeerBuckets
		}
		if row.LastSeen.After(g.lastSeen) {
			g.lastSeen = row.LastSeen
		}
	}

	for _, rec := range receipts {
		if rec.ContractResult != string(domain.ResultPass) && rec.ContractResult != string(domain.ResultFail) {
			continue // SKIPPED contracts assert nothing
		}
		_, g := get(rec.Env)
		sc := g.byStage[string(domain.StageContract)]
		if rec.ContractResult == string(domain.ResultPass) {
			sc.Pass++
		} else {
			sc.Fail++
		}
		g.byStage[string(domain.StageContract)] = sc
		g.verCount++
		g.recPeers[rec.PeerID] = true
		g.samples = append(g.samples, Sample{
			Class:  domain.ClassSampleVerification,
			Result: domain.Result(rec.ContractResult),
			Count:  1,
			Age:    now.Sub(rec.CreatedAt),
		})
		if rec.CreatedAt.After(g.lastSeen) {
			g.lastSeen = rec.CreatedAt
		}
	}

	keys := make([]bucketKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	// Context-first ordering: the execution-context label is the leading
	// dimension of every row key.
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].ContextLabel != keys[j].ContextLabel {
			return keys[i].ContextLabel < keys[j].ContextLabel
		}
		return keys[i].EnvHash < keys[j].EnvHash
	})

	rows := make([]SnapshotRow, 0, len(keys))
	for _, k := range keys {
		g := groups[k]
		independence := int64(g.maxPeers + len(g.recPeers))
		v := Compute(g.samples, independence)
		row := SnapshotRow{
			ContextLabel:           k.ContextLabel,
			EnvBucket:              g.bucket,
			ByStage:                g.byStage,
			ObservationClassCounts: map[string]int64{},
			VerificationCounts:     map[string]int64{},
			PassRate:               v.PassRate,
			Confidence:             v.Confidence,
			ElevatedFailure:        v.ElevatedFailure,
			UniquePeerBuckets:      int(independence),
		}
		if g.obsCount > 0 {
			row.ObservationClassCounts[string(domain.ClassUsageObservation)] = g.obsCount
		}
		if g.verCount > 0 {
			row.VerificationCounts[string(domain.ClassSampleVerification)] = g.verCount
		}
		if !g.lastSeen.IsZero() {
			row.LastSeen = g.lastSeen.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}

	return Snapshot{
		SchemaVersion:        1,
		PURL:                 purl,
		Symbol:               symbol,
		Rows:                 rows,
		Failures:             failureSummaries(evidence),
		RegressionCandidates: regressions,
		GeneratedAt:          now.UTC().Format(time.RFC3339),
	}
}

// failureSummaries groups FAIL evidence by (stage, fingerprint, code).
func failureSummaries(evidence []serverstore.EvidenceRow) []FailureSummary {
	type fkey struct{ stage, fp, code string }
	counts := map[fkey]int64{}
	envs := map[fkey][]domain.EnvironmentFingerprint{}
	for _, row := range evidence {
		if row.Result != string(domain.ResultFail) {
			continue
		}
		if row.ErrorFingerprint == "" && row.ErrorCode == "" {
			continue
		}
		k := fkey{row.Stage, row.ErrorFingerprint, row.ErrorCode}
		counts[k] += row.ObservationCount
		if env, ok := parseEnv(row.EnvJSON); ok {
			envs[k] = append(envs[k], env)
		}
	}
	keys := make([]fkey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		if keys[i].stage != keys[j].stage {
			return keys[i].stage < keys[j].stage
		}
		return keys[i].fp < keys[j].fp
	})
	out := make([]FailureSummary, 0, len(keys))
	for _, k := range keys {
		out = append(out, FailureSummary{
			Stage:       k.stage,
			ErrorCode:   k.code,
			Fingerprint: k.fp,
			Count:       counts[k],
			EnvSummary:  envSummary(envs[k]),
		})
	}
	return out
}
