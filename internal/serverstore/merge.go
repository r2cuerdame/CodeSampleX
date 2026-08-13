package serverstore

import "github.com/r2cuerdame/codesamplex/internal/domain"

// Delta-merge ingest semantics (BINDING, plan task P5.1; matches client
// re-send behavior):
//
// A client uploads its whole local aggregate row for the current epoch each
// time it syncs, so the same (bucket, agg row, epoch) may arrive many times
// with an equal or grown observationCount. Each evidence_dedup row therefore
// stores the bucket's LAST-CONTRIBUTED count; an incoming batch adds only
// delta = max(0, incoming - previous) to evidence_agg.observation_count.
// A re-sent identical batch adds exactly 0. unique_peer_buckets and
// unique_project_buckets are always the count of distinct dedup buckets of
// that kind for the agg row.
//
// pg.go implements exactly these semantics in SQL; mergeState is the pure
// reference implementation the unit tests hold it to.

// aggKey identifies one evidence_agg row — the UNIQUE(purl,symbol,env_hash,
// stage,result,error_fp) columns of contract C4.
type aggKey struct {
	PURL    string
	Symbol  string
	EnvHash string
	Stage   string
	Result  string
	ErrorFP string
}

// aggKeyOf derives the aggregate row key for a batch. The purl is
// canonicalized and the environment normalized so equivalent spellings from
// different clients land on the same row.
func aggKeyOf(b domain.ObservationBatch) aggKey {
	pkg := b.Package
	if p, err := domain.ParsePURL(b.Package); err == nil {
		pkg = p.String()
	}
	return aggKey{
		PURL:    pkg,
		Symbol:  b.Symbol,
		EnvHash: b.Environment.Normalize().Hash(),
		Stage:   string(b.Stage),
		Result:  string(b.Result),
		ErrorFP: b.ErrorFingerprint,
	}
}

// deltaContribution returns how much a bucket's newly reported epoch total
// adds to the aggregate given what it contributed before: only growth counts,
// and shrunken or repeated reports contribute nothing.
func deltaContribution(prev, incoming int64) int64 {
	if incoming <= prev {
		return 0
	}
	return incoming - prev
}

// contribKey identifies one dedup ledger entry: which peer bucket already
// contributed to which agg row in which epoch.
type contribKey struct {
	agg    aggKey
	epoch  string
	bucket string
}

// mergeState is the in-memory reference implementation of the delta-merge.
// It exists for tests (and any future memory-backed Store); the PostgreSQL
// implementation in pg.go must behave identically.
type mergeState struct {
	contributions  map[contribKey]int64 // peer bucket's last-contributed count
	observations   map[aggKey]int64     // evidence_agg.observation_count
	peerBuckets    map[aggKey]map[string]struct{}
	projectBuckets map[aggKey]map[string]struct{}
}

func newMergeState() *mergeState {
	return &mergeState{
		contributions:  map[contribKey]int64{},
		observations:   map[aggKey]int64{},
		peerBuckets:    map[aggKey]map[string]struct{}{},
		projectBuckets: map[aggKey]map[string]struct{}{},
	}
}

// apply merges one (already validated) batch and returns the delta actually
// added to the aggregate's observation count.
func (m *mergeState) apply(b domain.ObservationBatch) int64 {
	k := aggKeyOf(b)
	incoming := int64(b.ObservationCount)

	ck := contribKey{agg: k, epoch: b.Epoch, bucket: b.AnonID}
	delta := deltaContribution(m.contributions[ck], incoming)
	m.contributions[ck] = incoming
	m.observations[k] += delta

	if m.peerBuckets[k] == nil {
		m.peerBuckets[k] = map[string]struct{}{}
	}
	m.peerBuckets[k][b.AnonID] = struct{}{}
	if m.projectBuckets[k] == nil {
		m.projectBuckets[k] = map[string]struct{}{}
	}
	m.projectBuckets[k][b.ProjectBucket] = struct{}{}
	return delta
}
