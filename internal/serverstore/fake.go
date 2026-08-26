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
	searchHits map[string]SearchHitRow
	mu         sync.Mutex

	merge   *mergeState
	aggMeta map[aggKey]*fakeAggMeta
	// coresidence mirrors the version_coresidence table: one entry per
	// (library, pair), holding the project-days that saw it and whether a
	// build failed there with a named cause.
	coresidence map[coresKey]map[string]bool
	// edges mirrors dependency_edge: one entry per relationship, holding the
	// project-days that saw it.
	edges map[edgeKey]map[string]bool

	packages        map[string]PackageRow
	snapshots       map[[2]string]string
	snapshotAt      map[[2]string]time.Time
	cases           map[string]domain.Case
	samples         map[string]SampleRow
	receipts        map[string][]ReceiptRow
	jobs            []*JobRow
	nextJobID       int64
	peers           map[string]PeerRow
	shards          map[string][2]string // key → {etag, json}
	shardWrites     int                  // PutShard calls, for incremental-rebuild tests
	ids             map[string]IdentityRow
	clusters        map[fakeClusterKey]ClusterRow
	stats           map[string]string // day → stats JSON
	wanted          map[[5]string]*WantedRow
	adoptions       map[[3]string]AdoptionRow
	wantedSeen      map[[7]string]bool
	authoring       map[string]AuthoringSessionRow
	adminTokens     map[string]AdminTokenRow // token hash -> row
	authoringDrafts map[string]AuthoringDraftRow
	authoringWork   map[[4]string]AuthoringWorkRow
	// authoringAttempts is the bounded per-coordinate attempt ledger that
	// decides when a coordinate stops being offered. See
	// authoring_quarantine.go.
	authoringAttempts map[[4]string]*authoringLedger

	// NowFn is the test seam for time-dependent behavior; nil means time.Now.
	NowFn func() time.Time
	// ChangedSinceFn overrides change detection. The fake keeps no per-row
	// timestamps, so incremental-rebuild tests script it directly.
	ChangedSinceFn func(context.Context, time.Time) (Changes, error)
}

// coresKey identifies one version pair of one library.
type coresKey struct{ ecosystem, name, lo, hi string }

// edgeKey identifies one dependency relationship between exact versions.
type edgeKey struct {
	ecosystem, parentName, parentVersion, childName, childVersion string
}

type fakeAggMeta struct {
	symbolConfidence string
	envJSON          string
	errorCode        string
	terminationKind  string
	exitCode         *int
	signal           string
	timeoutMillis    int64
	errorSummary     string
	evidenceQuality  string
	outerCommands    map[string]bool
	outerStage       string
	actualToolchain  string
	stageEvidence    string
	evidenceGap      string
	// direct: the reporter listed this package in their own manifest. Chosen
	// wins and never unsays itself.
	direct    bool
	firstSeen time.Time
	lastSeen  time.Time
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
		merge:           newMergeState(),
		aggMeta:         map[aggKey]*fakeAggMeta{},
		coresidence:     map[coresKey]map[string]bool{},
		edges:           map[edgeKey]map[string]bool{},
		packages:        map[string]PackageRow{},
		snapshots:       map[[2]string]string{},
		snapshotAt:      map[[2]string]time.Time{},
		cases:           map[string]domain.Case{},
		samples:         map[string]SampleRow{},
		receipts:        map[string][]ReceiptRow{},
		peers:           map[string]PeerRow{},
		shards:          map[string][2]string{},
		ids:             map[string]IdentityRow{},
		clusters:        map[fakeClusterKey]ClusterRow{},
		stats:           map[string]string{},
		wanted:          map[[5]string]*WantedRow{},
		adoptions:       map[[3]string]AdoptionRow{},
		wantedSeen:      map[[7]string]bool{},
		authoring:       map[string]AuthoringSessionRow{},
		authoringDrafts: map[string]AuthoringDraftRow{},
		authoringWork:   map[[4]string]AuthoringWorkRow{},

		authoringAttempts: map[[4]string]*authoringLedger{},
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
			outerCommands:    map[string]bool{},
		}
		f.aggMeta[k] = meta
	}
	meta.direct = meta.direct || b.Direct
	// The pair the scanner saw, one entry per project-day. Recorded even when
	// nothing failed: a pair that never breaks is worth knowing about, and
	// the caller decides what to show.
	if p, err := domain.ParsePURL(b.Package); err == nil && b.ProjectBucket != "" {
		projectDay := b.ProjectBucket + "" + b.Epoch
		for _, pair := range coresidencePairs(b) {
			ck := coresKey{p.Ecosystem, p.Name, pair.Lower, pair.Higher}
			if f.coresidence[ck] == nil {
				f.coresidence[ck] = map[string]bool{}
			}
			f.coresidence[ck][projectDay] = f.coresidence[ck][projectDay] ||
				batchNamesAnAttributedFailure(b)
		}
	}
	// Who pulled what, one entry per project-day.
	if b.ProjectBucket != "" {
		projectDay := b.ProjectBucket + "" + b.Epoch
		for _, pair := range edgeClaims(b) {
			parent, child := pair[0], pair[1]
			ek := edgeKey{parent.Ecosystem, parent.Name, parent.Version, child.Name, child.Version}
			if f.edges[ek] == nil {
				f.edges[ek] = map[string]bool{}
			}
			f.edges[ek][projectDay] = true
		}
	}
	if meta.errorCode == "" {
		meta.errorCode = b.ErrorCode
	}
	if meta.terminationKind == "" {
		meta.terminationKind = string(b.TerminationKind)
		meta.exitCode = b.ExitCode
		meta.signal = b.Signal
		meta.timeoutMillis = b.TimeoutMillis
	}
	if meta.errorSummary == "" {
		meta.errorSummary = b.ErrorSummary
	}
	if meta.evidenceQuality == "" {
		meta.evidenceQuality = normalizedEvidenceQuality(b)
	}
	if b.OuterCommand != "" {
		meta.outerCommands[b.OuterCommand] = true
	}
	if meta.outerStage == "" {
		meta.outerStage = string(b.OuterStage)
		meta.actualToolchain = b.ActualToolchain
		meta.stageEvidence = string(b.StageEvidence)
		meta.evidenceGap = string(b.FailureEvidenceGap)
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

func (f *Fake) ListSnapshots(_ context.Context) ([]SnapshotRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SnapshotRow, 0, len(f.snapshots))
	for key, snapshotJSON := range f.snapshots {
		out = append(out, SnapshotRow{PURL: key[0], Symbol: key[1], SnapshotJSON: snapshotJSON})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

func (f *Fake) PutSnapshot(_ context.Context, purl, symbol, snapshotJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshotAt[[2]string{purl, symbol}] = f.now()
	f.snapshots[[2]string{purl, symbol}] = snapshotJSON
	return nil
}

func (f *Fake) SnapshotKeys(_ context.Context) ([]SnapshotTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SnapshotTarget, 0, len(f.snapshots))
	for key := range f.snapshots {
		out = append(out, SnapshotTarget{PURL: key[0], Symbol: key[1]})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

func (f *Fake) DeleteSnapshots(_ context.Context, targets []SnapshotTarget) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, target := range targets {
		delete(f.snapshots, [2]string{target.PURL, target.Symbol})
	}
	return nil
}

// ChangedSince reports everything as changed. The fake keeps no per-row
// timestamps, and over-reporting is the safe direction: a builder test then
// exercises the full path, and no test can pass because a change was
// silently missed.
func (f *Fake) ChangedSince(ctx context.Context, since time.Time) (Changes, error) {
	if f.ChangedSinceFn != nil {
		return f.ChangedSinceFn(ctx, since)
	}
	targets, err := f.ListSnapshotTargets(ctx)
	if err != nil {
		return Changes{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	for sampleID, s := range f.samples {
		var m struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(s.ManifestJSON), &m) == nil {
			for _, purl := range m.Packages {
				seen[purl] = true
			}
		}
		for _, receipt := range f.receipts[sampleID] {
			for _, purl := range resolvedPackageStrings(receipt.ReceiptJSON) {
				seen[purl] = true
			}
		}
	}
	purls := make([]string, 0, len(seen))
	for purl := range seen {
		purls = append(purls, purl)
	}
	sort.Strings(purls)
	return Changes{Targets: targets, SamplePURLs: purls}, nil
}

func (f *Fake) SnapshotUpdatedAt(_ context.Context) (map[string]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]time.Time{}
	for k := range f.snapshots {
		at, ok := f.snapshotAt[k]
		if !ok {
			continue
		}
		if prev, seen := out[k[0]]; !seen || at.After(prev) {
			out[k[0]] = at
		}
	}
	return out, nil
}

func (f *Fake) ListSnapshotTargets(_ context.Context) ([]SnapshotTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[SnapshotTarget]bool{}
	var out []SnapshotTarget
	var claims []receiptClaim
	for k := range f.aggMeta {
		t := SnapshotTarget{PURL: k.PURL, Symbol: k.Symbol}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	// A signed v2 receipt establishes an exact package target even before
	// anonymous evidence exists for it. v1 and empty resolved lists establish
	// no version and therefore add no target.
	for sampleID, receipts := range f.receipts {
		sample, ok := f.samples[sampleID]
		if !ok || sample.Quarantined {
			continue
		}
		var manifest struct {
			Symbols []string `json:"symbols"`
			Subject string   `json:"subject"`
		}
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, receipt := range receipts {
			// Same filter as PG: only a v2 receipt whose resolve PASSED
			// establishes what a sample resolved. Without it a v1 receipt,
			// or one whose resolve failed, still filed the manifest's
			// symbols — a target production would never create.
			if !receiptEstablishesClaim(receipt.ReceiptJSON) {
				continue
			}
			claims = append(claims, receiptClaim{
				Packages: resolvedPackageStrings(receipt.ReceiptJSON),
				Symbols:  manifest.Symbols,
				Subject:  manifest.Subject,
			})
		}
	}
	// Attributed together, never per receipt: the narrowest claim on a symbol
	// cannot be known until every claim has been seen.
	for _, t := range snapshotTargetsFromClaims(claims) {
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

// resolvedPackageStrings reads only exact versions established by a v2
// receipt whose resolve stage passed. Corrupt lists fail closed as a unit;
// callers never salvage one convenient version from an invalid claim.
func resolvedPackageStrings(receiptJSON string) []string {
	var receipt domain.VerificationReceipt
	if json.Unmarshal([]byte(receiptJSON), &receipt) != nil ||
		receipt.SchemaVersion != 2 ||
		receipt.Stages["resolve"] != string(domain.ResultPass) ||
		len(receipt.ResolvedPackages) == 0 {
		return nil
	}
	out := make([]string, 0, len(receipt.ResolvedPackages))
	for i, raw := range receipt.ResolvedPackages {
		p, err := domain.ParsePURL(raw)
		if err != nil || p.String() != raw || !domain.ConcreteResolvedVersion(p.Version) ||
			(i > 0 && raw <= receipt.ResolvedPackages[i-1]) {
			return nil
		}
		out = append(out, raw)
	}
	return out
}

func (f *Fake) EvidenceForTarget(_ context.Context, purl, symbol string) ([]EvidenceRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []EvidenceRow
	// One symbol is filed under both the scanner's spelling and the author's;
	// see symbolSpellings. Both stores ask the same question through it.
	want := map[string]bool{}
	for _, s := range symbolSpellings(purl, symbol) {
		want[s] = true
	}
	for k, meta := range f.aggMeta {
		if k.PURL != purl || !want[k.Symbol] {
			continue
		}
		outerCommands := make([]string, 0, len(meta.outerCommands))
		for command := range meta.outerCommands {
			outerCommands = append(outerCommands, command)
		}
		sort.Strings(outerCommands)
		outerCommand := ""
		if len(outerCommands) > 0 {
			outerCommand = outerCommands[0]
		}
		out = append(out, EvidenceRow{
			PURL: k.PURL, Symbol: k.Symbol,
			SymbolConfidence: meta.symbolConfidence,
			EnvHash:          k.EnvHash, EnvJSON: meta.envJSON,
			Stage: k.Stage, Result: k.Result,
			ErrorFingerprint: k.ErrorFP, ErrorCode: meta.errorCode, Direct: meta.direct,
			TerminationKind: meta.terminationKind, ExitCode: meta.exitCode,
			Signal: meta.signal, TimeoutMillis: meta.timeoutMillis,
			ErrorSummary: meta.errorSummary, EvidenceQuality: meta.evidenceQuality,
			OuterCommand: outerCommand, OuterCommands: outerCommands, OuterStage: meta.outerStage,
			ActualToolchain: meta.actualToolchain, StageEvidence: meta.stageEvidence,
			FailureEvidenceGap:   meta.evidenceGap,
			ObservationCount:     f.merge.observations[k],
			UniquePeerBuckets:    peakBuckets(f.merge.peerBuckets, k),
			UniqueProjectBuckets: peakBuckets(f.merge.projectBuckets, k),
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

// statusRank orders the C13 lifecycle so a re-publish cannot walk it back.
func statusRank(status string) int {
	switch status {
	case "STABLE":
		return 4
	case "MATRIX_PASS":
		return 3
	case "CROSS_PASS":
		return 2
	case "LOCAL_PASS":
		return 1
	}
	return 0
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
	if prev, ok := f.samples[s.SampleID]; ok {
		if !prev.CreatedAt.IsZero() && s.CreatedAt.IsZero() {
			s.CreatedAt = prev.CreatedAt
		}
		// Mirrors the SQL conflict path: ingest never rewrites lifecycle
		// status. Same id means the same immutable artifact; only receipt
		// processing may move it.
		s.Status = prev.Status
		s.Quarantined, s.QuarantineReason = prev.Quarantined, prev.QuarantineReason
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

// SamplesForPackages filters the fake's samples by package prefix,
// mirroring the SQL LIKE ANY match.
func (f *Fake) SamplesForPackages(ctx context.Context, patterns []string, limit int) ([]SampleRow, error) {
	// ListSamples(0) means "use the default", which is 50 -- so this
	// filtered the newest FIFTY and called the result a search over the
	// store. Postgres does the opposite: it filters in SQL and applies the
	// limit after, precisely so relevance stops being a function of
	// publication order (see pg.go). A fake that caps first cannot fail the
	// test that would catch that regression, and the identical line three
	// methods down was already fixed for exactly this reason.
	all, err := f.ListSamples(ctx, 1<<30)
	if err != nil {
		return nil, err
	}
	var out []SampleRow
	for _, row := range all {
		var m struct {
			Packages []string `json:"packages"`
		}
		if json.Unmarshal([]byte(row.ManifestJSON), &m) != nil {
			continue
		}
		if matchesAnyPattern(m.Packages, patterns) {
			out = append(out, row)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *Fake) VerifiedSamplesForPackages(ctx context.Context, patterns []string, limit int) ([]SampleRow, error) {
	all, err := f.SamplesForPackages(ctx, patterns, 1<<30)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SampleRow, 0, len(all))
	for _, row := range all {
		if !f.hasContractPass(row.SampleID, "") {
			continue
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *Fake) VerifiedSampleCodeCounts(_ context.Context, packagePrefix string) ([]VerifiedSampleCodeCount, error) {
	if packagePrefix == "" {
		return nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	counts := map[[2]string]int64{}
	for _, sample := range f.samples {
		if sample.Quarantined || !f.hasContractPass(sample.SampleID, "") {
			continue
		}
		var manifest domain.SampleManifest
		if json.Unmarshal([]byte(sample.ManifestJSON), &manifest) != nil {
			continue
		}
		// Count a coordinate at most once per sample even if malformed author
		// input repeats a package or symbol. PostgreSQL uses the same DISTINCT
		// sample boundary.
		seen := map[[2]string]bool{}
		for _, purl := range manifest.Packages {
			if !strings.HasPrefix(purl, packagePrefix) {
				continue
			}
			seen[[2]string{purl, ""}] = true
			for _, symbol := range manifest.Symbols {
				if symbol != "" {
					seen[[2]string{purl, symbol}] = true
				}
			}
		}
		for key := range seen {
			counts[key]++
		}
	}

	out := make([]VerifiedSampleCodeCount, 0, len(counts))
	for key, count := range counts {
		out = append(out, VerifiedSampleCodeCount{PURL: key[0], Symbol: key[1], Samples: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PURL != out[j].PURL {
			return out[i].PURL < out[j].PURL
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

func (f *Fake) ListVerifiedSamples(ctx context.Context, limit int) ([]SampleRow, error) {
	all, err := f.ListSamples(ctx, 1<<30)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SampleRow, 0, len(all))
	for _, row := range all {
		if !f.hasContractPass(row.SampleID, "") {
			continue
		}
		out = append(out, row)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ListVerifiedBeliefSamples mirrors the PG query, keyset and all, so a test
// written against the Fake proves something about the server.
//
// The ordering is the SQL's: newest first, with sample_id DESC as the
// tiebreak, because that pair is what the cursor compares. A missing
// created_at sorts as the epoch here for the same reason it does there.
func (f *Fake) ListVerifiedBeliefSamples(ctx context.Context, after SampleCursor, limit int) ([]SampleRow, error) {
	if limit <= 0 {
		limit = 200
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []SampleRow
	for _, s := range f.samples {
		if s.Quarantined {
			continue
		}
		if !f.hasContractPass(s.SampleID, "") {
			continue
		}
		if manifestBelief(s.ManifestJSON) == "" {
			continue
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := CursorFor(out[i]), CursorFor(out[j])
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.SampleID > b.SampleID
	})
	if !after.IsZero() {
		kept := out[:0]
		for _, s := range out {
			if cursorBefore(CursorFor(s), after) {
				kept = append(kept, s)
			}
		}
		out = kept
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// cursorBefore is the Go form of the SQL's
// (created_at, sample_id) < (cursor.created_at, cursor.sample_id).
func cursorBefore(c, than SampleCursor) bool {
	if !c.CreatedAt.Equal(than.CreatedAt) {
		return c.CreatedAt.Before(than.CreatedAt)
	}
	return c.SampleID < than.SampleID
}

// manifestBelief reads the declared belief out of a manifest, matching the
// SQL's manifest->'case'->>'believed'. Unparseable JSON states nothing.
func manifestBelief(manifestJSON string) string {
	var m struct {
		Case struct {
			Believed string `json:"believed"`
		} `json:"case"`
	}
	if json.Unmarshal([]byte(manifestJSON), &m) != nil {
		return ""
	}
	return m.Case.Believed
}

// matchesAnyPattern implements the "prefix%" form the SQL uses.
func matchesAnyPattern(purls, patterns []string) bool {
	for _, p := range purls {
		for _, pat := range patterns {
			if strings.HasPrefix(p, strings.TrimSuffix(pat, "%")) {
				return true
			}
		}
	}
	return false
}

func (f *Fake) SetSampleQuarantine(_ context.Context, sampleID string, on bool, reason string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.samples[sampleID]
	if !ok {
		return fmt.Errorf("serverstore: no sample %s", sampleID)
	}
	row.Quarantined = on
	row.QuarantineReason = reason
	f.samples[sampleID] = row
	return nil
}

func (f *Fake) ListSamples(_ context.Context, limit int) ([]SampleRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	out := make([]SampleRow, 0, len(f.samples))
	for _, s := range f.samples {
		if s.Quarantined {
			continue // mirrors the SQL's WHERE NOT quarantined
		}
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

func (f *Fake) SaveReceiptForJob(_ context.Context, r ReceiptRow, jobID int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var job *JobRow
	for _, candidate := range f.jobs {
		if candidate.ID == jobID {
			job = candidate
			break
		}
	}
	if job == nil || job.Status != "claimed" || job.ClaimedBy != r.PeerID || job.SampleID != r.SampleID {
		return false, nil
	}
	for _, prev := range f.receipts[r.SampleID] {
		if prev.ReceiptID == r.ReceiptID {
			return false, nil // a signed receipt cannot consume a second job
		}
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = f.now()
	}
	f.receipts[r.SampleID] = append(f.receipts[r.SampleID], r)
	job.Status = "done"
	if r.ContractResult == "PASS" && job.Reason == "cross" {
		// The draft row must exist — this is authoring output, not an
		// anonymous upload — but a live assignment must not be required:
		// the writing session expires long before some verifications land,
		// and its absence says nothing about whether the contract ran.
		_, hasDraft := f.authoringDrafts[r.SampleID]
		if sample, ok := f.samples[r.SampleID]; ok && hasDraft && sample.Status == "DRAFT" && sample.Quarantined {
			sample.Status = "CROSS_PASS"
			sample.Quarantined = false
			sample.QuarantineReason = ""
			f.samples[r.SampleID] = sample
		}
	} else if r.ContractResult == "FAIL" && job.Reason == "cross" {
		for key, work := range f.authoringWork {
			if work.SampleID == r.SampleID {
				delete(f.authoringWork, key)
			}
		}
	}
	return true, nil
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
	if j.Reason == "matrix" {
		for _, existing := range f.jobs {
			if existing.SampleID == j.SampleID && existing.Reason == j.Reason && existing.WantEnvJSON == j.WantEnvJSON {
				return existing.ID, nil
			}
		}
	}
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

func (f *Fake) OpenJobs(ctx context.Context, capability, peerID, reason string, limit int) ([]JobRow, error) {
	return f.OpenJobsPage(ctx, capability, peerID, reason, "", limit, 0)
}

func (f *Fake) OpenJobsPage(_ context.Context, capability, peerID, reason, verifierOS string, limit, offset int) ([]JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	var out []JobRow
	eligible := 0
	for _, j := range f.jobs {
		// A claim that outlived JobLease is claimable again — otherwise a
		// peer that died holding a job holds it forever.
		expired := j.Status == "claimed" && !j.ClaimedAt.IsZero() &&
			f.now().Sub(j.ClaimedAt) > JobLease
		if j.Status != "open" && !expired {
			continue
		}
		if reason != "" && j.Reason != reason {
			continue
		}
		// A peer that already JUDGED this sample cannot cross-verify it;
		// offering the job only takes it from someone who could. A receipt
		// whose contract never ran judged nothing — see ContractWasJudged.
		if peerID != "" && j.Reason == "cross" {
			var mine bool
			for _, r := range f.receipts[j.SampleID] {
				if r.PeerID == peerID && ContractWasJudged(r.ContractResult) {
					mine = true
					break
				}
			}
			if mine {
				continue
			}
		}
		if j.WantEnvJSON != "" {
			var want map[string]any
			if json.Unmarshal([]byte(j.WantEnvJSON), &want) == nil {
				if capability != "" {
					if pinned, ok := want["sandboxCapability"].(string); ok && pinned != capability {
						continue
					}
				}
				// A job names the platform its sample needs. Offering a Linux
				// verifier the Windows rows fills its window with work it cannot
				// run, and the one Windows verifier waits behind that. A job that
				// names no OS runs anywhere and is never hidden.
				if verifierOS != "" {
					if pinned, ok := want["os"].(string); ok && pinned != "" &&
						!strings.EqualFold(pinned, verifierOS) {
						continue
					}
				}
			}
		}
		if eligible < offset {
			eligible++
			continue
		}
		eligible++
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

// StrandedDrafts lists quarantined authoring drafts that have no verification
// left to wait for: no passing receipt, no open or claimed job, and fewer than
// maxAttempts cross jobs already spent.
func (f *Fake) StrandedDrafts(_ context.Context, maxAttempts, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	passed := map[string]bool{}
	for sampleID, rows := range f.receipts {
		for _, r := range rows {
			if r.ContractResult == "PASS" {
				passed[sampleID] = true
				break
			}
		}
	}
	live := map[string]bool{}
	attempts := map[string]int{}
	for _, j := range f.jobs {
		if j.Status == "open" || j.Status == "claimed" {
			live[j.SampleID] = true
		}
		if j.Reason == "cross" {
			attempts[j.SampleID]++
		}
	}
	ids := make([]string, 0, len(f.samples))
	for id := range f.samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []string
	for _, id := range ids {
		s := f.samples[id]
		if s.Status != "DRAFT" || !s.Quarantined {
			continue
		}
		if passed[id] || live[id] || attempts[id] >= maxAttempts {
			continue
		}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *Fake) Job(_ context.Context, id int64) (JobRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID == id {
			return *j, true, nil
		}
	}
	return JobRow{}, false, nil
}

func (f *Fake) CrossJobsForLaneReview(_ context.Context, limit int) ([]JobRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	var out []JobRow
	for _, j := range f.jobs {
		if j.Reason != "cross" {
			continue
		}
		expired := j.Status == "claimed" &&
			(j.ClaimedAt.IsZero() || f.now().Sub(j.ClaimedAt) > JobLease)
		if j.Status != "open" && j.Status != JobStatusUnsupported && !expired {
			continue
		}
		out = append(out, *j)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (f *Fake) SetJobRequirements(_ context.Context, id int64, wantEnvJSON, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID != id {
			continue
		}
		expired := j.Status == "claimed" &&
			(j.ClaimedAt.IsZero() || f.now().Sub(j.ClaimedAt) > JobLease)
		if j.Status != "open" && j.Status != JobStatusUnsupported && !expired {
			return nil
		}
		j.WantEnvJSON = wantEnvJSON
		j.Status = status
		j.ClaimedBy = ""
		j.ClaimedAt = time.Time{}
		return nil
	}
	return nil
}

func (f *Fake) ClaimJob(_ context.Context, id int64, peerID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, j := range f.jobs {
		if j.ID != id {
			continue
		}
		expired := j.Status == "claimed" && !j.ClaimedAt.IsZero() &&
			f.now().Sub(j.ClaimedAt) > JobLease
		if j.Status == "open" || expired {
			for _, other := range f.jobs {
				if other.ID != j.ID && other.SampleID == j.SampleID && other.Status == "claimed" &&
					other.ClaimedBy == peerID && !other.ClaimedAt.IsZero() && f.now().Sub(other.ClaimedAt) <= JobLease {
					return false, nil
				}
			}
			j.Status = "claimed"
			j.ClaimedBy = peerID
			j.ClaimedAt = f.now()
			return true, nil
		}
	}
	return false, nil
}

func (f *Fake) CompleteJobsForSample(_ context.Context, sampleID, peerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// A cross job asks a SECOND peer to reproduce the result, so the
	// origin's own receipt does not answer it. The origin is the peer of
	// the sample's first receipt, matching sampleStatusFromReceipts.
	var origin string
	if rs := f.receipts[sampleID]; len(rs) > 0 {
		origin = rs[0].PeerID
		for _, r := range rs[1:] {
			if r.CreatedAt.Before(rs[0].CreatedAt) {
				origin = r.PeerID
			}
		}
	}
	for _, j := range f.jobs {
		if j.SampleID != sampleID || j.Reason != "cross" || (j.Status != "open" && j.Status != "claimed") {
			continue
		}
		if j.Reason == "cross" && peerID != "" && peerID == origin {
			continue
		}
		j.Status = "done"
	}
	return nil
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

// ShardWrites counts PutShard calls so tests can assert that an idle
// aggregation pass does no work — the whole point of incremental rebuilds.
func (f *Fake) ShardWrites() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shardWrites
}

func (f *Fake) PutShard(_ context.Context, key, etag, shardJSON string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shards[key] = [2]string{etag, shardJSON}
	f.shardWrites++
	return nil
}

// ListSamplesPage is ListSamples with an offset.
func (f *Fake) ListSamplesPage(ctx context.Context, limit, offset int) ([]SampleRow, error) {
	// Every row, then the window. ListSamples(0) means "use the default",
	// which is 50 -- so this paged through the newest 50 and returned
	// nothing at all past that, quietly capping the very scan that exists
	// to stop quiet capping.
	all, err := f.ListSamples(ctx, 1<<30)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= len(all) {
		return nil, nil
	}
	all = all[offset:]
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SamplesBySeeder lists one seeder's live samples, newest first.
func (f *Fake) SamplesBySeeder(ctx context.Context, login string, limit int) ([]SampleRow, error) {
	// ListSamples(0) means "use the default", which is 50 -- so this
	// filtered the newest FIFTY and called the result a search over the
	// store. Postgres does the opposite: it filters in SQL and applies the
	// limit after, precisely so relevance stops being a function of
	// publication order (see pg.go). A fake that caps first cannot fail the
	// test that would catch that regression, and the identical line three
	// methods down was already fixed for exactly this reason.
	all, err := f.ListSamples(ctx, 1<<30)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	var out []SampleRow
	for _, s := range all {
		if s.OriginSeeder != login {
			continue
		}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// ShardKeys lists the stored keys in sorted order.
func (f *Fake) ShardKeys(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.shards))
	for k := range f.shards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// HotShardKeys returns the stored keys in sorted order — the fake has no
// traffic model, only determinism.
func (f *Fake) HotShardKeys(_ context.Context, limit int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.shards))
	for k := range f.shards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}

func (f *Fake) GetShard(_ context.Context, key string) (string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.shards[key]
	return v[0], v[1], ok, nil
}

func (f *Fake) GetShardEtag(_ context.Context, key string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.shards[key]
	return v[0], ok, nil
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
		if c.FirstSeen.IsZero() || (!prev.FirstSeen.IsZero() && prev.FirstSeen.Before(c.FirstSeen)) {
			c.FirstSeen = prev.FirstSeen
		}
	} else {
		c.ID = int64(len(f.clusters) + 1)
		if c.FirstSeen.IsZero() {
			c.FirstSeen = now
		}
	}
	if c.LastSeen.IsZero() {
		c.LastSeen = now
	}
	f.clusters[k] = c
	return nil
}

func (f *Fake) ListFailureClusters(_ context.Context, packageName string) ([]ClusterRow, error) {
	return f.listFailureClusters(packageName, true)
}

// ListFailureClustersIncludingPreserved mirrors the PostgreSQL read of the
// same name: exact failure matching still needs the pre-0024 fingerprints.
func (f *Fake) ListFailureClustersIncludingPreserved(_ context.Context, packageName string) ([]ClusterRow, error) {
	return f.listFailureClusters(packageName, false)
}

func (f *Fake) listFailureClusters(packageName string, currentOnly bool) ([]ClusterRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ClusterRow
	for _, c := range f.clusters {
		// Same rule as PostgreSQL: preserved pre-0024 rows stay stored and
		// stay out of the reads that describe live clusters. A Fake that
		// served them everywhere let a doubled cluster ledger pass a green
		// suite.
		if c.PackageName == packageName && (!currentOnly || IsCurrentFailureCluster(c)) {
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

// VersionCoresidence lists the version pairs of one library that a scanner
// saw in a single resolution.
func (f *Fake) VersionCoresidence(_ context.Context, ecosystem, name string) ([]VersionCoresidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	type key struct{ lo, hi string }
	projects := map[key]map[string]bool{}
	failing := map[key]map[string]bool{}
	for k, seen := range f.coresidence {
		if k.ecosystem != ecosystem || k.name != name {
			continue
		}
		pk := key{k.lo, k.hi}
		if projects[pk] == nil {
			projects[pk] = map[string]bool{}
			failing[pk] = map[string]bool{}
		}
		for projectDay, failed := range seen {
			projects[pk][projectDay] = true
			if failed {
				failing[pk][projectDay] = true
			}
		}
	}
	out := make([]VersionCoresidence, 0, len(projects))
	for pk, seen := range projects {
		out = append(out, VersionCoresidence{
			Lower: pk.lo, Higher: pk.hi,
			Projects: len(seen), Failing: len(failing[pk]),
		})
	}
	// Same read-side healing as PG, so the two stores keep one behaviour.
	return canonicalCoresidencePairs(out), nil
}

// Dependants lists what pulled each version of one library.
func (f *Fake) Dependants(_ context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []DependencyEdge
	for k, projectDays := range f.edges {
		if k.ecosystem != ecosystem || k.childName != name {
			continue
		}
		out = append(out, DependencyEdge{
			ParentName: k.parentName, ParentVersion: k.parentVersion,
			ChildName: k.childName, ChildVersion: k.childVersion,
			Projects: len(projectDays),
		})
	}
	sortDependencyEdges(out)
	return out, nil
}

// Dependencies lists what shipped ALONGSIDE each version of one package.
//
// Upgrade a library and its dependencies move under you; the one that moved
// is usually the one that broke the build. Same table as Dependants, read
// from the parent end.
func (f *Fake) Dependencies(_ context.Context, ecosystem, name string) ([]DependencyEdge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []DependencyEdge
	for k, projectDays := range f.edges {
		if k.ecosystem != ecosystem || k.parentName != name {
			continue
		}
		out = append(out, DependencyEdge{
			ParentName: k.parentName, ParentVersion: k.parentVersion,
			ChildName: k.childName, ChildVersion: k.childVersion,
			Projects: len(projectDays),
		})
	}
	sortShippedWith(out)
	return out, nil
}

// isRunStage reports whether a stage records something being exercised, as
// opposed to merely being present. It is the same split the compatibility
// grid draws between a pass rate and a usage count.
func isRunStage(stage string) bool {
	return strings.HasPrefix(stage, "PROJECT")
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
	// Distinct project buckets. Those rotate monthly rather than daily, so
	// PG windows them to the current month; the fake keeps no epoch for
	// them and counts every one it has seen, which is the same answer for
	// any test that does not span a month boundary.
	projects := map[string]struct{}{}
	for _, buckets := range f.merge.projectBuckets {
		for b := range buckets {
			projects[b] = struct{}{}
		}
	}
	c.ProjectsMonth = int64(len(projects))
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
		// Package-level rows only: the same build is written once for the
		// package and again for every symbol detected in it, and counting
		// both makes one build look like several.
		//
		// And runs only. USED records that a package was PRESENT, not that
		// anything was exercised — it has no failing form and carried 8,686
		// of production's 42,808 package-level events, so counting it made
		// "observations" partly a head count of installed dependencies.
		if k.Symbol == "" && isRunStage(k.Stage) {
			c.Observations += f.merge.observations[k]
		}
	}
	c.Symbols = int64(len(symbols))
	for id, s := range f.samples {
		if verifiedStatus(s.Status) || f.hasContractPass(id, "") {
			c.VerifiedSamples++
		}
	}
	return c, nil
}

// hasContractPass reports whether any receipt proved the sample's contract.
func (f *Fake) hasContractPass(sampleID, targetOS string) bool {
	for _, r := range f.receipts[sampleID] {
		if strings.EqualFold(r.ContractResult, "PASS") && receiptAnswersOS(r.ReceiptJSON, targetOS) {
			return true
		}
	}
	return false
}

// receiptAnswersOS reports whether a receipt can answer a request for
// targetOS. An unpinned request ("") is a question about the package, so any
// platform answers it; a pinned one is a question about that platform, and a
// pass from elsewhere is a different measurement wearing the same name.
func receiptAnswersOS(receiptJSON, targetOS string) bool {
	if targetOS == "" {
		return true
	}
	return strings.EqualFold(farmReceiptOS(receiptJSON), targetOS)
}

// hasExactResolvedContractPass is the versioned Wanted answer boundary.
// A manifest version is author input; only a PASS receipt whose valid v2
// resolvedPackages list names the coordinate proves that release actually
// ran. Legacy v1 receipts intentionally establish no exact version.
func (f *Fake) hasExactResolvedContractPass(sampleID, purl, targetOS string) bool {
	for _, r := range f.receipts[sampleID] {
		if !strings.EqualFold(r.ContractResult, "PASS") || !receiptAnswersOS(r.ReceiptJSON, targetOS) {
			continue
		}
		for _, resolved := range resolvedPackageStrings(r.ReceiptJSON) {
			if resolved == purl {
				return true
			}
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

// ----------------------------------------------------------------- wanted --

func (f *Fake) RecordWanted(ctx context.Context, epoch, anonID string, rows []WantedRow) error {
	return f.RecordWantedBatch(ctx, []WantedSubmission{{Epoch: epoch, AnonID: anonID, Rows: rows}})
}

func (f *Fake) RecordWantedBatch(_ context.Context, reports []WantedSubmission) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, report := range reports {
		for _, r := range report.Rows {
			seen := [7]string{r.Ecosystem, r.Name, r.Version, r.Symbol, r.TargetOS, report.Epoch, report.AnonID}
			if f.wantedSeen[seen] {
				continue
			}
			f.wantedSeen[seen] = true
			key := [5]string{r.Ecosystem, r.Name, r.Version, r.Symbol, r.TargetOS}
			w := f.wanted[key]
			if w == nil {
				w = &WantedRow{Ecosystem: r.Ecosystem, Name: r.Name, Version: r.Version, Symbol: r.Symbol,
					TargetOS: r.TargetOS, FirstSeen: f.now()}
				f.wanted[key] = w
			}
			w.Asks++
			w.LastSeen = f.now()
		}
	}
	return nil
}

func (f *Fake) TopWanted(_ context.Context, limit int) ([]WantedRow, error) {
	rows, _, err := f.listWanted("", 0, limit, "", "")
	return rows, err
}

func (f *Fake) ListWanted(_ context.Context, query string, offset, limit int) ([]WantedRow, int, error) {
	return f.listWanted(query, offset, limit, "", "")
}

func (f *Fake) WantedForPackage(_ context.Context, ecosystem, name string) ([]WantedRow, error) {
	rows, _, err := f.listWanted("", 0, 100, ecosystem, name)
	return rows, err
}

func (f *Fake) listWanted(query string, offset, limit int, ecosystem, name string) ([]WantedRow, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	words := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	var out []WantedRow
	for _, w := range f.wanted {
		if ecosystem != "" && (w.Ecosystem != ecosystem || w.Name != name) {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{w.Ecosystem, w.Name, w.Version, w.Symbol}, " "))
		matches := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		answered := false
		for _, s := range f.samples {
			if s.Quarantined {
				continue
			}
			var m struct {
				Packages []string `json:"packages"`
				Symbols  []string `json:"symbols"`
			}
			if json.Unmarshal([]byte(s.ManifestJSON), &m) != nil {
				continue
			}
			packageMatch := false
			for _, ps := range m.Packages {
				pp, err := domain.ParsePURL(ps)
				if err != nil || pp.Ecosystem != w.Ecosystem || pp.Name != w.Name {
					continue
				}
				packageMatch = true
			}
			if !packageMatch {
				continue
			}
			// A row that names a platform is answered only by a proof from
			// that platform. Closing it on any pass would delete the ask
			// before the platform it was about had been measured at all.
			if w.Version == "" {
				if !f.hasContractPass(s.SampleID, w.TargetOS) {
					continue
				}
			} else {
				exact := domain.PURL{Ecosystem: w.Ecosystem, Name: w.Name, Version: w.Version}.String()
				if !f.hasExactResolvedContractPass(s.SampleID, exact, w.TargetOS) {
					continue
				}
			}
			symbolMatch := w.Symbol == ""
			for _, symbol := range m.Symbols {
				if symbol == w.Symbol {
					symbolMatch = true
					break
				}
			}
			if symbolMatch {
				answered = true
				break
			}
		}
		if answered {
			continue
		}
		row := *w
		row.HasPage = true
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Asks != out[j].Asks {
			return out[i].Asks > out[j].Asks
		}
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		if out[i].Version != out[j].Version {
			return out[i].Version < out[j].Version
		}
		return out[i].Symbol < out[j].Symbol
	})
	total := len(out)
	if offset >= total {
		return nil, total, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// -------------------------------------------------------------- adoptions --

// RecordSearchHit counts one search that found something, deduplicated the
// way postgres does: once per reporter per offer per day.
func (f *Fake) RecordSearchHit(_ context.Context, r SearchHitRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	dedup := r.OfferID
	if dedup == "" {
		dedup = r.SampleID
	}
	if f.searchHits == nil {
		f.searchHits = map[string]SearchHitRow{}
	}
	f.searchHits[r.Epoch+"|"+r.AnonID+"|"+dedup] = r
	return nil
}

func (f *Fake) RecordAdoption(_ context.Context, r AdoptionRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := [3]string{r.SampleID, r.Epoch, r.AnonID}
	if prev, ok := f.adoptions[key]; ok && r.BuildPass == nil {
		r.BuildPass = prev.BuildPass
	}
	f.adoptions[key] = r
	return nil
}

func (f *Fake) AdoptionSummary(_ context.Context) (AdoptionCounts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var c AdoptionCounts
	for _, r := range f.adoptions {
		c.Reports++
		if r.Applied {
			c.Applied++
		}
		if r.BuildPass != nil {
			if *r.BuildPass {
				c.BuildPass++
			} else {
				c.BuildFail++
			}
		}
	}
	return c, nil
}
