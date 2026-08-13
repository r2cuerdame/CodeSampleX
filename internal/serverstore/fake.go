package serverstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// Fake is a complete in-memory Store implementation. Handler and builder
// tests (and e2e harnesses that want a server without PostgreSQL) use it;
// its ingest semantics are the mergeState reference implementation that
// pg.go is held to, so both stores behave identically.
type Fake struct {
	mu sync.Mutex

	merge   *mergeState
	aggMeta map[aggKey]*fakeAggMeta

	packages  map[string]PackageRow
	snapshots map[[2]string]string
	cases     map[string]domain.Case
	samples   map[string]SampleRow
	receipts  map[string][]ReceiptRow
	jobs      []*JobRow
	nextJobID int64
	peers     map[string]PeerRow
	shards    map[string][2]string // key → {etag, json}
	ids       map[string]IdentityRow
	clusters  map[fakeClusterKey]ClusterRow
	stats     map[string]string // day → stats JSON

	// NowFn is the test seam for time-dependent behavior; nil means time.Now.
	NowFn func() time.Time
}

type fakeAggMeta struct {
	symbolConfidence string
	envJSON          string
	errorCode        string
	firstSeen        time.Time
	lastSeen         time.Time
}

type fakeClusterKey struct {
	Ecosystem   string
	PackageName string
	Symbol      string
	Stage       string
	ErrorFP     string
}

var _ Store = (*Fake)(nil)

// NewFake returns an empty in-memory Store.
func NewFake() *Fake {
	return &Fake{
		merge:     newMergeState(),
		aggMeta:   map[aggKey]*fakeAggMeta{},
		packages:  map[string]PackageRow{},
		snapshots: map[[2]string]string{},
		cases:     map[string]domain.Case{},
		samples:   map[string]SampleRow{},
		receipts:  map[string][]ReceiptRow{},
		peers:     map[string]PeerRow{},
		shards:    map[string][2]string{},
		ids:       map[string]IdentityRow{},
		clusters:  map[fakeClusterKey]ClusterRow{},
		stats:     map[string]string{},
	}
}

func (f *Fake) now() time.Time {
	if f.NowFn != nil {
		return f.NowFn()
	}
	return time.Now().UTC()
}

// Close implements Store; the fake has nothing to release.
func (f *Fake) Close() {}

// ---------------------------------------------------------------- ingest --

// IngestBatches mirrors PG.IngestBatches: validate, then delta-merge.
func (f *Fake) IngestBatches(_ context.Context, batches []domain.ObservationBatch) (int, []RejectedBatch, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	accepted := 0
	var rejected []RejectedBatch
	for i, b := range batches {
		if err := ValidateBatch(b); err != nil {
			rejected = append(rejected, RejectedBatch{Index: i, Reason: err.Error()})
			continue
		}
		f.ingestOneLocked(b)
		accepted++
	}
	return accepted, rejected, nil
}

func (f *Fake) ingestOneLocked(b domain.ObservationBatch) {
	purl, _ := domain.ParsePURL(b.Package) // already validated
	canonical := purl.String()
	now := f.now()

	if pkg, ok := f.packages[canonical]; ok {
		pkg.LastSeen = now
		f.packages[canonical] = pkg
	} else {
		f.packages[canonical] = PackageRow{
			PURL: canonical, Ecosystem: purl.Ecosystem, Name: purl.Name,
			Version: purl.Version, Major: purl.Major(), Publicness: "UNKNOWN",
			FirstSeen: now, LastSeen: now,
		}
	}

	k := aggKeyOf(b)
	meta := f.aggMeta[k]
	if meta == nil {
		env := b.Environment.Normalize()
		confidence := string(b.SymbolConfidence)
		if confidence == "" {
			confidence = string(domain.SymbolUnknown)
		}
		meta = &fakeAggMeta{
			symbolConfidence: confidence,
			envJSON:          string(domain.MustCanonicalJSON(env)),
			firstSeen:        now,
		}
		f.aggMeta[k] = meta
	}
	if meta.errorCode == "" {
		meta.errorCode = b.ErrorCode
	}
	meta.lastSeen = now
	f.merge.apply(b)
}

// PurgeDedupOlderThan drops rotating dedup ledger entries older than the
// retention window; accumulated counts and unique-bucket sets stay frozen,
// matching pg.go.
func (f *Fake) PurgeDedupOlderThan(_ context.Context, days int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cutoff := f.now().AddDate(0, 0, -days).Format("2006-01-02")
	var removed int64
	for ck := range f.merge.contributions {
		if ck.epoch < cutoff {
			delete(f.merge.contributions, ck)
			removed++
		}
	}
	return removed, nil
}

// -------------------------------------------------------------- packages --

func (f *Fake) UpsertPackage(_ context.Context, p PackageRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Publicness == "" {
		p.Publicness = "UNKNOWN"
	}
	now := f.now()
	if prev, ok := f.packages[p.PURL]; ok {
		prev.Publicness = p.Publicness
		prev.CheckedAt = p.CheckedAt
		prev.LastSeen = now
		f.packages[p.PURL] = prev
		return nil
	}
	if p.FirstSeen.IsZero() {
		p.FirstSeen = now
	}
	if p.LastSeen.IsZero() {
		p.LastSeen = now
	}
	f.packages[p.PURL] = p
	return nil
}

func (f *Fake) GetPackage(_ context.Context, purl string) (PackageRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.packages[purl]
	return p, ok, nil
}

func (f *Fake) ListPackageVersions(_ context.Context, ecosystem, name string) ([]PackageRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []PackageRow
	for _, p := range f.packages {
		if p.Ecosystem == ecosystem && p.Name == name {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].Version > out[j].Version
	})
	return out, nil
}

// ------------------------------------------------------------- snapshots --

func (f *Fake) GetSnapshot(_ context.Context, purl, symbol string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	js, ok := f.snapshots[[2]string{purl, symbol}]
	return js, ok, nil
}

func (f *Fake) PutSnapshot(_ context.Context, purl, symbol, snapshotJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots[[2]string{purl, symbol}] = snapshotJSON
	return nil
}

func (f *Fake) ListSnapshotTargets(_ context.Context) ([]SnapshotTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[SnapshotTarget]bool{}
	var out []SnapshotTarget
	for k := range f.aggMeta {
		t := SnapshotTarget{PURL: k.PURL, Symbol: k.Symbol}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

func (f *Fake) EvidenceForTarget(_ context.Context, purl, symbol string) ([]EvidenceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []EvidenceRow
	for k, meta := range f.aggMeta {
		if k.PURL != purl || k.Symbol != symbol {
			continue
		}
		out = append(out, EvidenceRow{
			PURL: k.PURL, Symbol: k.Symbol,
			SymbolConfidence: meta.symbolConfidence,
			EnvHash:          k.EnvHash, EnvJSON: meta.envJSON,
			Stage: k.Stage, Result: k.Result,
			ErrorFingerprint: k.ErrorFP, ErrorCode: meta.errorCode,
			ObservationCount:     f.merge.observations[k],
			UniquePeerBuckets:    len(f.merge.peerBuckets[k]),
			UniqueProjectBuckets: len(f.merge.projectBuckets[k]),
			FirstSeen:            meta.firstSeen, LastSeen: meta.lastSeen,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.EnvHash != b.EnvHash {
			return a.EnvHash < b.EnvHash
		}
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		if a.Result != b.Result {
			return a.Result < b.Result
		}
		return a.ErrorFingerprint < b.ErrorFingerprint
	})
	return out, nil
}

// -------------------------------------------------------- cases + samples --

func (f *Fake) SaveCase(_ context.Context, c domain.Case) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.CaseID == "" {
		c.CaseID = c.ComputeID()
	}
	f.cases[c.CaseID] = c
	return nil
}

func (f *Fake) SaveSample(_ context.Context, s SampleRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Status == "" {
		s.Status = "PUBLISHED"
	}
	if s.License == "" {
		s.License = "MIT-0"
	}
	if prev, ok := f.samples[s.SampleID]; ok && !prev.CreatedAt.IsZero() && s.CreatedAt.IsZero() {
		s.CreatedAt = prev.CreatedAt
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = f.now()
	}
	f.samples[s.SampleID] = s
	return nil
}

func (f *Fake) GetSample(_ context.Context, sampleID string) (SampleRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.samples[sampleID]
	return s, ok, nil
}

func (f *Fake) ListSamples(_ context.Context, limit int) ([]SampleRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	out := make([]SampleRow, 0, len(f.samples))
	for _, s := range f.samples {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].SampleID < out[j].SampleID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *Fake) SetSampleStatus(_ context.Context, sampleID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.samples[sampleID]
	if !ok {
		return fmt.Errorf("serverstore: sample %s not found", sampleID)
	}
	s.Status = status
	f.samples[sampleID] = s
	return nil
}

// --------------------------------------------------------------- receipts --

func (f *Fake) SaveReceipt(_ context.Context, r ReceiptRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, prev := range f.receipts[r.SampleID] {
		if prev.ReceiptID == r.ReceiptID {
			return nil // immutable: ON CONFLICT DO NOTHING
		}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = f.now()
	}
	f.receipts[r.SampleID] = append(f.receipts[r.SampleID], r)
	return nil
}

func (f *Fake) ReceiptsForSample(_ context.Context, sampleID string) ([]ReceiptRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReceiptRow(nil), f.receipts[sampleID]...), nil
}

// ------------------------------------------------------------------- jobs --

func (f *Fake) CreateJob(_ context.Context, j JobRow) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextJobID++
	j.ID = f.nextJobID
	if j.Status == "" {
		j.Status = "open"
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = f.now()
	}
	f.jobs = append(f.jobs, &j)
	return j.ID, nil
}

func (f *Fake) OpenJobs(_ context.Context, capability string, limit int) ([]JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	var out []JobRow
	for _, j := range f.jobs {
		if j.Status != "open" {
			continue
		}
		if capability != "" && j.WantEnvJSON != "" {
			var want map[string]any
			if json.Unmarshal([]byte(j.WantEnvJSON), &want) == nil {
				if pinned, ok := want["sandboxCapability"].(string); ok && pinned != capability {
					continue
				}
			}
		}
		out = append(out, *j)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *Fake) JobsForSample(_ context.Context, sampleID string) ([]JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []JobRow
	for _, j := range f.jobs {
		if j.SampleID == sampleID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (f *Fake) ClaimJob(_ context.Context, id int64, peerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID == id && j.Status == "open" {
			j.Status = "claimed"
			j.ClaimedBy = peerID
			j.ClaimedAt = f.now()
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) CompleteJob(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID == id {
			j.Status = "done"
		}
	}
	return nil
}

// ------------------------------------------------------------------ peers --

func (f *Fake) AnnouncePeer(_ context.Context, p PeerRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.AnnouncedAt.IsZero() {
		p.AnnouncedAt = f.now()
	}
	f.peers[p.PeerID] = p
	return nil
}

func (f *Fake) PeersForSample(_ context.Context, sampleID string) ([]PeerRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	var out []PeerRow
	for _, p := range f.peers {
		if !p.ExpiresAt.After(now) {
			continue
		}
		var ids []string
		if p.SampleIDsJSON != "" {
			_ = json.Unmarshal([]byte(p.SampleIDsJSON), &ids)
		}
		for _, id := range ids {
			if id == sampleID {
				out = append(out, p)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AnnouncedAt.After(out[j].AnnouncedAt) })
	return out, nil
}

func (f *Fake) ExpirePeers(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var removed int64
	for id, p := range f.peers {
		if !p.ExpiresAt.After(now) {
			delete(f.peers, id)
			removed++
		}
	}
	return removed, nil
}

// ----------------------------------------------------------------- shards --

func (f *Fake) PutShard(_ context.Context, key, etag, shardJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shards[key] = [2]string{etag, shardJSON}
	return nil
}

func (f *Fake) GetShard(_ context.Context, key string) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.shards[key]
	return v[0], v[1], ok, nil
}

// ------------------------------------------------------------- identities --

func (f *Fake) SaveIdentity(_ context.Context, login string, githubID int64, tokenHash, apiTokenHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.ids[login]
	if !ok {
		row = IdentityRow{Login: login, Display: login, CreatedAt: f.now()}
	}
	row.GithubID = githubID
	row.TokenHash = tokenHash
	row.APITokenHash = apiTokenHash
	f.ids[login] = row
	return nil
}

func (f *Fake) IdentityByAPIToken(_ context.Context, apiTokenHash string) (IdentityRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if apiTokenHash == "" {
		return IdentityRow{}, false, nil
	}
	for _, row := range f.ids {
		if row.APITokenHash == apiTokenHash {
			return row, true, nil
		}
	}
	return IdentityRow{}, false, nil
}

// --------------------------------------------------------------- clusters --

func (f *Fake) UpsertFailureCluster(_ context.Context, c ClusterRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fakeClusterKey{c.Ecosystem, c.PackageName, c.Symbol, c.Stage, c.ErrorFingerprint}
	now := f.now()
	if prev, ok := f.clusters[k]; ok {
		c.ID = prev.ID
		c.FirstSeen = prev.FirstSeen
	} else {
		c.ID = int64(len(f.clusters) + 1)
		if c.FirstSeen.IsZero() {
			c.FirstSeen = now
		}
	}
	c.LastSeen = now
	f.clusters[k] = c
	return nil
}

func (f *Fake) ListFailureClusters(_ context.Context, packageName string) ([]ClusterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ClusterRow
	for _, c := range f.clusters {
		if c.PackageName == packageName {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ObservationCount != out[j].ObservationCount {
			return out[i].ObservationCount > out[j].ObservationCount
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ------------------------------------------------------------------ stats --

func (f *Fake) SetStatsDaily(_ context.Context, day string, statsJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats[day] = statsJSON
	return nil
}

func (f *Fake) GetLatestStats(_ context.Context) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	best := ""
	for day := range f.stats {
		if day > best {
			best = day
		}
	}
	if best == "" {
		return "", false, nil
	}
	return f.stats[best], true, nil
}

func (f *Fake) NetworkCounts(_ context.Context, now time.Time) (NetworkCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var c NetworkCounts
	for _, p := range f.peers {
		if p.ExpiresAt.After(now) {
			c.ServingPeers++
		}
	}
	// Peers = distinct anonymous buckets that contributed evidence this
	// epoch, mirroring the PG query.
	epoch := now.UTC().Format("2006-01-02")
	active := map[string]struct{}{}
	for ck := range f.merge.contributions {
		if ck.epoch == epoch {
			active[ck.bucket] = struct{}{}
		}
	}
	c.Peers = int64(len(active))
	pkgNames := map[string]bool{}
	for _, p := range f.packages {
		pkgNames[p.Ecosystem+"\x00"+p.Name] = true
	}
	c.Packages = int64(len(pkgNames))
	symbols := map[string]bool{}
	for k := range f.aggMeta {
		if k.Symbol != "" && !symbols[k.Symbol] {
			symbols[k.Symbol] = true
		}
		c.Observations += f.merge.observations[k]
	}
	c.Symbols = int64(len(symbols))
	for id, s := range f.samples {
		if verifiedStatus(s.Status) || f.hasContractPass(id) {
			c.VerifiedSamples++
		}
	}
	return c, nil
}

// hasContractPass reports whether any receipt proved the sample's contract.
func (f *Fake) hasContractPass(sampleID string) bool {
	for _, r := range f.receipts[sampleID] {
		if strings.EqualFold(r.ContractResult, "PASS") {
			return true
		}
	}
	return false
}

// verifiedStatus reports whether a sample status is CROSS_PASS or beyond.
// A contract-PASS receipt counts as verified too (see NetworkCounts): the
// status ladder is about independent reproduction, not about whether the
// sample was ever proven to work.
func verifiedStatus(status string) bool {
	switch strings.ToUpper(status) {
	case "CROSS_PASS", "MATRIX_PASS", "STABLE":
		return true
	}
	return false
}
