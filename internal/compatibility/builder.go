package compatibility

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Builder is the server aggregation pipeline: it materializes compatibility
// snapshots, failure clusters, regression flags, C6 shards, matrix
// verification jobs and the daily stats rollup from raw evidence + receipts.
// Web/API reads only ever touch the materialized outputs (§14.5).
type Builder struct {
	Store serverstore.Store
	// Now is a test seam; nil means time.Now.
	Now func() time.Time

	// lastRun and passes drive incremental rebuilds. RunLoop is the only
	// caller and is single-goroutine, so these need no locking.
	lastRun time.Time
	passes  int
}

// Incremental rebuild bounds.
const (
	// fullPassEvery forces a complete rebuild periodically so the
	// materialized views self-heal from any missed change — a bug in the
	// change query would otherwise leave a stale shard stale forever. At
	// the default 5-minute interval this is hourly.
	fullPassEvery = 12

	// changeOverlap re-examines a little before the last pass started.
	// Rows written while a pass was running would otherwise fall in the
	// gap between "last_seen <= passStart" and "> passStart", and be
	// picked up by neither pass.
	changeOverlap = time.Minute
)

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now().UTC()
}

// RunLoop runs RunOnce immediately, then on every interval tick until ctx is
// canceled. Failures are logged and retried on the next tick — an outage
// never kills the loop (goal.md §3.9 resilience posture).
func (b *Builder) RunLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if err := b.RunOnce(ctx); err != nil && ctx.Err() == nil {
		log.Printf("compatibility: builder run: %v", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.RunOnce(ctx); err != nil && ctx.Err() == nil {
				log.Printf("compatibility: builder run: %v", err)
			}
		}
	}
}

// pkgKey identifies a package across versions.
type pkgKey struct{ ecosystem, name string }

// symVer indexes evidence rows by symbol → version.
type symVer = map[string]map[string][]serverstore.EvidenceRow

// sampleData is one sample with its parsed manifest and receipts.
type sampleData struct {
	row      serverstore.SampleRow
	manifest domain.SampleManifest
	purls    []domain.PURL
	receipts []ReceiptInfo
}

// RunOnce executes one aggregation pass.
//
// The pass is INCREMENTAL by default. Rebuilding everything on every tick
// cost 1,603 shard writes and 1,958 snapshot writes every five minutes on
// a network where zero evidence rows had changed — work that scales with
// the size of the whole network rather than with what happened, and the
// first thing that would flatten a small instance as the graph grows.
//
// Every fullPassEvery-th pass rebuilds everything anyway, so a missed
// change repairs itself rather than leaving a shard permanently stale.
func (b *Builder) RunOnce(ctx context.Context) error {
	now := b.now()
	passStart := now
	full := b.lastRun.IsZero() || b.passes%fullPassEvery == 0

	// affected limits the rebuild to shard keys touched since the last
	// pass; nil means "everything", which is what a full pass wants.
	var affected map[shardKey]bool
	if !full {
		changes, cerr := b.Store.ChangedSince(ctx, b.lastRun.Add(-changeOverlap))
		if cerr != nil {
			return fmt.Errorf("compatibility: changes since %s: %w", b.lastRun, cerr)
		}
		if changes.Empty() {
			// Nothing moved. Stats still refresh — they are one query and
			// they carry the clock the website displays.
			if err := b.refreshStats(ctx, now); err != nil {
				return err
			}
			b.passes++
			b.lastRun = passStart
			return nil
		}
		affected = affectedKeys(changes)
	}

	targets, err := b.Store.ListSnapshotTargets(ctx)
	if err != nil {
		return fmt.Errorf("compatibility: list targets: %w", err)
	}
	if affected != nil {
		targets = keepTargets(targets, affected)
	}
	samples, err := b.loadSamples(ctx)
	if err != nil {
		return err
	}

	// Evidence indexed by package → symbol → version → rows.
	byPkg := map[pkgKey]symVer{}
	purlOf := map[pkgKey]map[string]string{} // version → purl string
	for _, t := range targets {
		p, perr := domain.ParsePURL(t.PURL)
		if perr != nil {
			continue
		}
		rows, eerr := b.Store.EvidenceForTarget(ctx, t.PURL, t.Symbol)
		if eerr != nil {
			return fmt.Errorf("compatibility: evidence for %s %q: %w", t.PURL, t.Symbol, eerr)
		}
		k := pkgKey{p.Ecosystem, p.Name}
		if byPkg[k] == nil {
			byPkg[k] = symVer{}
			purlOf[k] = map[string]string{}
		}
		if byPkg[k][t.Symbol] == nil {
			byPkg[k][t.Symbol] = map[string][]serverstore.EvidenceRow{}
		}
		byPkg[k][t.Symbol][p.Version] = rows
		purlOf[k][p.Version] = t.PURL
	}

	regressionsByPkg := map[pkgKey][]RegressionCandidate{}

	// Snapshots per target, with §10.3 regression detection against V-1.
	for _, t := range targets {
		p, perr := domain.ParsePURL(t.PURL)
		if perr != nil {
			continue
		}
		k := pkgKey{p.Ecosystem, p.Name}
		rows := byPkg[k][t.Symbol][p.Version]

		var regs []RegressionCandidate
		versions := make([]string, 0, len(byPkg[k][t.Symbol]))
		for v := range byPkg[k][t.Symbol] {
			versions = append(versions, v)
		}
		if prevVer, ok := PreviousVersion(versions, p.Version); ok {
			prevPURL := purlOf[k][prevVer]
			regs = DetectRegressions(t.PURL, prevPURL, t.Symbol,
				rows, byPkg[k][t.Symbol][prevVer])
			regressionsByPkg[k] = append(regressionsByPkg[k], regs...)
		}

		receipts := receiptsForTarget(samples, p, t.Symbol)
		snap := BuildSnapshot(t.PURL, t.Symbol, rows, receipts, regs, now)
		js, jerr := json.Marshal(snap)
		if jerr != nil {
			return fmt.Errorf("compatibility: marshal snapshot %s: %w", t.PURL, jerr)
		}
		if err := b.Store.PutSnapshot(ctx, t.PURL, t.Symbol, string(js)); err != nil {
			return fmt.Errorf("compatibility: put snapshot %s: %w", t.PURL, err)
		}
	}

	// Failure clusters per package (across versions and symbols).
	pkgKeys := make([]pkgKey, 0, len(byPkg))
	for k := range byPkg {
		pkgKeys = append(pkgKeys, k)
	}
	sort.Slice(pkgKeys, func(i, j int) bool {
		if pkgKeys[i].ecosystem != pkgKeys[j].ecosystem {
			return pkgKeys[i].ecosystem < pkgKeys[j].ecosystem
		}
		return pkgKeys[i].name < pkgKeys[j].name
	})
	for _, k := range pkgKeys {
		evidenceByVersion := map[string][]serverstore.EvidenceRow{}
		for _, versions := range byPkg[k] {
			for version, rows := range versions {
				evidenceByVersion[version] = append(evidenceByVersion[version], rows...)
			}
		}
		for _, cluster := range BuildClusters(k.ecosystem, k.name, evidenceByVersion, regressionsByPkg[k], now) {
			if err := b.Store.UpsertFailureCluster(ctx, cluster); err != nil {
				return fmt.Errorf("compatibility: upsert cluster %s/%s: %w", k.ecosystem, k.name, err)
			}
		}
	}

	// C6 shards per (ecosystem, name, major).
	if err := b.regenerateShards(ctx, byPkg, purlOf, samples, affected, now); err != nil {
		return err
	}

	// Matrix jobs for CROSS_PASS+ samples (§10.2 one-variable-changed).
	if err := b.createMatrixJobs(ctx, samples); err != nil {
		return err
	}

	if err := b.refreshStats(ctx, now); err != nil {
		return err
	}
	b.passes++
	b.lastRun = passStart
	return nil
}

// refreshStats writes the daily rollup. It runs on every pass, including
// passes with nothing else to do: it is a single query, and it is what the
// website's counters and generatedAt timestamp come from.
func (b *Builder) refreshStats(ctx context.Context, now time.Time) error {
	counts, err := b.Store.NetworkCounts(ctx, now)
	if err != nil {
		return fmt.Errorf("compatibility: network counts: %w", err)
	}
	statsJSON, err := StatsJSON(counts, 0, now)
	if err != nil {
		return err
	}
	if err := b.Store.SetStatsDaily(ctx, now.Format("2006-01-02"), string(statsJSON)); err != nil {
		return fmt.Errorf("compatibility: set stats: %w", err)
	}
	return nil
}

// shardKey identifies one materialized shard.
type shardKey struct{ ecosystem, name, major string }

func keyFor(purl string) (shardKey, bool) {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return shardKey{}, false
	}
	return shardKey{p.Ecosystem, p.Name, p.Major()}, true
}

// affectedKeys maps a change set onto the shards it can alter. A shard
// covers every symbol and sample of one package major, so a single changed
// row marks the whole key dirty — the unit of rebuild is the shard.
func affectedKeys(c serverstore.Changes) map[shardKey]bool {
	out := map[shardKey]bool{}
	for _, t := range c.Targets {
		if k, ok := keyFor(t.PURL); ok {
			out[k] = true
		}
	}
	for _, purl := range c.SamplePURLs {
		if k, ok := keyFor(purl); ok {
			out[k] = true
		}
	}
	return out
}

// keepTargets narrows the snapshot targets to the dirty shards. Every
// target of a dirty key is kept, not just the changed ones: a shard
// carries all of a package's symbols, so rebuilding it needs all of them.
func keepTargets(targets []serverstore.SnapshotTarget, affected map[shardKey]bool) []serverstore.SnapshotTarget {
	out := targets[:0:0]
	for _, t := range targets {
		if k, ok := keyFor(t.PURL); ok && affected[k] {
			out = append(out, t)
		}
	}
	return out
}

func (b *Builder) loadSamples(ctx context.Context) ([]sampleData, error) {
	rows, err := b.Store.ListSamples(ctx, 1000)
	if err != nil {
		return nil, fmt.Errorf("compatibility: list samples: %w", err)
	}
	out := make([]sampleData, 0, len(rows))
	for _, row := range rows {
		var manifest domain.SampleManifest
		if json.Unmarshal([]byte(row.ManifestJSON), &manifest) != nil {
			continue
		}
		sd := sampleData{row: row, manifest: manifest}
		for _, ps := range manifest.Packages {
			if p, perr := domain.ParsePURL(ps); perr == nil {
				sd.purls = append(sd.purls, p)
			}
		}
		receiptRows, rerr := b.Store.ReceiptsForSample(ctx, row.SampleID)
		if rerr != nil {
			return nil, fmt.Errorf("compatibility: receipts for %s: %w", row.SampleID, rerr)
		}
		for _, rr := range receiptRows {
			if info, ok := ParseReceiptRow(rr); ok {
				sd.receipts = append(sd.receipts, info)
			}
		}
		out = append(out, sd)
	}
	return out, nil
}

// receiptsForTarget collects receipts of samples that cover the target
// package (same ecosystem+name+major) and symbol ("" = package level;
// otherwise the sample must claim the symbol).
func receiptsForTarget(samples []sampleData, p domain.PURL, symbol string) []ReceiptInfo {
	var out []ReceiptInfo
	for _, sd := range samples {
		if !sampleCoversPackage(sd, p) {
			continue
		}
		if symbol != "" && !sampleClaimsSymbol(sd, symbol) {
			continue
		}
		out = append(out, sd.receipts...)
	}
	return out
}

func sampleCoversPackage(sd sampleData, p domain.PURL) bool {
	for _, sp := range sd.purls {
		if sp.Ecosystem == p.Ecosystem && sp.Name == p.Name && sp.Major() == p.Major() {
			return true
		}
	}
	return false
}

func sampleClaimsSymbol(sd sampleData, symbol string) bool {
	for _, s := range sd.manifest.Symbols {
		if s == symbol {
			return true
		}
	}
	return false
}

// regenerateShards rebuilds shards. affected limits it to the dirty keys;
// nil rebuilds every key present in the inputs (a full pass).
func (b *Builder) regenerateShards(ctx context.Context,
	byPkg map[pkgKey]symVer,
	purlOf map[pkgKey]map[string]string,
	samples []sampleData, affected map[shardKey]bool, now time.Time) error {

	shardPkgs := map[shardKey]map[string]*ShardPackage{} // purl → package entry

	for k, symbols := range byPkg {
		for symbol, versions := range symbols {
			for version, rows := range versions {
				purlStr := purlOf[k][version]
				p, err := domain.ParsePURL(purlStr)
				if err != nil {
					continue
				}
				sk := shardKey{k.ecosystem, k.name, p.Major()}
				if shardPkgs[sk] == nil {
					shardPkgs[sk] = map[string]*ShardPackage{}
				}
				entry := shardPkgs[sk][purlStr]
				if entry == nil {
					entry = &ShardPackage{PURL: purlStr}
					shardPkgs[sk][purlStr] = entry
				}
				if symbol == "" {
					continue // package-level evidence carries no symbol entry
				}
				stats, failures := SymbolStatsFromEvidence(rows, now)
				entry.Symbols = append(entry.Symbols, ShardSymbol{
					Family: symbol, Stats: stats, Failures: failures,
				})
			}
		}
	}

	// Samples also define shards. Deriving keys from observation evidence
	// alone meant a package with a verified sample but nothing observed yet
	// got no shard at all — and clients only ever read shards, so the
	// sample was invisible to every one of them. That is the normal state
	// for a freshly seeded package: the answer exists before the usage.
	for _, sd := range samples {
		for _, p := range sd.purls {
			sk := shardKey{p.Ecosystem, p.Name, p.Major()}
			if affected != nil && !affected[sk] {
				continue // clean key: its shard is already correct
			}
			if shardPkgs[sk] == nil {
				shardPkgs[sk] = map[string]*ShardPackage{}
			}
			if shardPkgs[sk][p.String()] == nil {
				shardPkgs[sk][p.String()] = &ShardPackage{PURL: p.String()}
			}
		}
	}

	keys := make([]shardKey, 0, len(shardPkgs))
	for sk := range shardPkgs {
		keys = append(keys, sk)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, c := keys[i], keys[j]
		if a.ecosystem != c.ecosystem {
			return a.ecosystem < c.ecosystem
		}
		if a.name != c.name {
			return a.name < c.name
		}
		return a.major < c.major
	})

	for _, sk := range keys {
		var pkgs []ShardPackage
		purls := make([]string, 0, len(shardPkgs[sk]))
		for purl := range shardPkgs[sk] {
			purls = append(purls, purl)
		}
		sort.Strings(purls)
		for _, purl := range purls {
			entry := shardPkgs[sk][purl]
			sort.Slice(entry.Symbols, func(i, j int) bool {
				return entry.Symbols[i].Family < entry.Symbols[j].Family
			})
			entry.Samples = shardSamplesFor(samples, sk.ecosystem, sk.name, sk.major)
			pkgs = append(pkgs, *entry)
		}
		key := sk.ecosystem + "/" + sk.name + "/" + sk.major
		shardJSON, etag := BuildShard(key, pkgs, now)
		if err := b.Store.PutShard(ctx, key, etag, shardJSON); err != nil {
			return fmt.Errorf("compatibility: put shard %s: %w", key, err)
		}
	}
	return nil
}

// shardSamplesFor renders the top samples covering (ecosystem, name, major),
// each with the contract stages its latest receipt actually reported.
func shardSamplesFor(samples []sampleData, ecosystem, name, major string) []ShardSample {
	var in []ShardSampleInput
	for _, sd := range samples {
		covered := false
		for _, sp := range sd.purls {
			if sp.Ecosystem == ecosystem && sp.Name == name && sp.Major() == major {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		entry := ShardSample{
			SampleID:    sd.row.SampleID,
			Goal:        sd.manifest.Case.Goal,
			Status:      sd.row.Status,
			License:     sd.row.License,
			Environment: sd.manifest.Environment,
		}
		if len(sd.receipts) > 0 {
			latest := sd.receipts[0]
			for _, r := range sd.receipts[1:] {
				if r.CreatedAt.After(latest.CreatedAt) {
					latest = r
				}
			}
			entry.ContractStages = latest.Stages
		}
		in = append(in, ShardSampleInput{
			Sample:    entry,
			HotScore:  sd.row.HotScore,
			CreatedAt: sd.row.CreatedAt,
		})
	}
	return TopShardSamples(in)
}

// createMatrixJobs opens up to 3 one-variable-changed verification jobs for
// every sample at CROSS_PASS or beyond (§10.2): each wantEnv changes exactly
// one of os, runtime major, or browser family relative to the environments
// already covered by receipts.
func (b *Builder) createMatrixJobs(ctx context.Context, samples []sampleData) error {
	for _, sd := range samples {
		if !isVerifiedStatus(sd.row.Status) || len(sd.receipts) == 0 {
			continue
		}
		existing, err := b.Store.JobsForSample(ctx, sd.row.SampleID)
		if err != nil {
			return fmt.Errorf("compatibility: jobs for %s: %w", sd.row.SampleID, err)
		}
		matrixCount := 0
		existingWant := map[string]bool{}
		for _, j := range existing {
			if j.Reason == "matrix" {
				matrixCount++
				existingWant[j.WantEnvJSON] = true
			}
		}
		if matrixCount >= 3 {
			continue
		}
		for _, want := range matrixWantEnvs(sd.receipts, 3-matrixCount, existingWant) {
			if _, err := b.Store.CreateJob(ctx, serverstore.JobRow{
				SampleID:    sd.row.SampleID,
				Reason:      "matrix",
				WantEnvJSON: want,
				Status:      "open",
			}); err != nil {
				return fmt.Errorf("compatibility: create matrix job for %s: %w", sd.row.SampleID, err)
			}
		}
	}
	return nil
}

func isVerifiedStatus(status string) bool {
	switch status {
	case "CROSS_PASS", "MATRIX_PASS", "STABLE":
		return true
	}
	return false
}

// matrixWantEnvs proposes up to max one-variable-changed environment deltas
// not yet covered by receipts and not already requested.
func matrixWantEnvs(receipts []ReceiptInfo, max int, existing map[string]bool) []string {
	coveredOS := map[string]bool{}
	coveredRuntimeMajor := map[string]bool{}
	coveredBrowser := map[string]bool{}
	runtimeName := ""
	browserSeen := false
	for _, r := range receipts {
		if r.Env.OS != "" {
			coveredOS[r.Env.OS] = true
		}
		if r.Env.Runtime != "" {
			runtimeName = r.Env.Runtime
			coveredRuntimeMajor[majorOf(r.Env.RuntimeVersion)] = true
		}
		if r.Env.BrowserFamily != "" {
			browserSeen = true
			coveredBrowser[r.Env.BrowserFamily] = true
		}
	}

	var out []string
	add := func(m map[string]string) bool {
		js := string(domain.MustCanonicalJSON(m))
		if existing[js] {
			return len(out) < max
		}
		existing[js] = true
		out = append(out, js)
		return len(out) < max
	}

	for _, os := range []string{"linux", "windows", "darwin"} {
		if !coveredOS[os] {
			if !add(map[string]string{"os": os}) {
				return out
			}
		}
	}
	if runtimeName != "" {
		for _, major := range []string{"22", "24", "20", "18"} {
			if !coveredRuntimeMajor[major] {
				if !add(map[string]string{"runtime": runtimeName, "runtimeMajor": major}) {
					return out
				}
				break // one runtime-major delta is enough per pass
			}
		}
	}
	if browserSeen {
		for _, fam := range []string{"chrome", "firefox", "safari"} {
			if !coveredBrowser[fam] {
				if !add(map[string]string{"browserFamily": fam}) {
					return out
				}
			}
		}
	}
	return out
}

func majorOf(version string) string {
	for i := 0; i < len(version); i++ {
		if version[i] == '.' {
			return version[:i]
		}
	}
	return version
}

// EstimatedStat is an explicitly-labeled estimate: Estimated is ALWAYS true
// and the assumptions ride along (goal.md dashboard honesty rules).
type EstimatedStat struct {
	Estimated   bool     `json:"estimated"` // always true
	Value       int64    `json:"value"`
	Formula     string   `json:"formula"`
	Assumptions []string `json:"assumptions"`
}

// PlaceholderStat is a metric we cannot measure yet, labeled as such.
type PlaceholderStat struct {
	Value float64 `json:"value"`
	Note  string  `json:"note"`
}

// StatsDoc is the daily stats rollup served by GET /v1/stats.
// Field names are a public contract shared with the website and the CLI —
// renaming one silently blanks a landing-page counter, so they stay fixed:
// peers, packages, symbols, evidence, verifiedSamples, postHitSuccessRate,
// estimatedReasoningAvoided (always flagged estimated), estimated,
// generatedAt.
type StatsDoc struct {
	SchemaVersion int    `json:"schemaVersion"`
	Day           string `json:"day"`
	GeneratedAt   string `json:"generatedAt"`
	// Peers is TODAY's distinct anonymous peer buckets. Those rotate
	// daily, so this genuinely cannot be summed over time — a peer active
	// all month would appear as thirty. ProjectsMonth is the honest
	// participation figure that does not reset at midnight.
	Peers         int64 `json:"peers"`
	ProjectsMonth int64 `json:"projectsMonth"`
	Packages      int64 `json:"packages"`
	Symbols       int64 `json:"symbols"`
	// Evidence counts observation records, not peers or projects: a big
	// number here says "widely used", never "widely verified".
	Evidence        int64 `json:"evidence"`
	VerifiedSamples int64 `json:"verifiedSamples"`
	// PostHitSuccessRate is 0..1; PostHitBuildPass keeps the honest note
	// that no adoption data has been collected yet.
	PostHitSuccessRate        float64         `json:"postHitSuccessRate"`
	PostHitBuildPass          PlaceholderStat `json:"postHitBuildPass"`
	EstimatedReasoningAvoided EstimatedStat   `json:"estimatedReasoningAvoided"`
	// Estimated marks the whole document as containing estimated figures,
	// mirroring EstimatedReasoningAvoided.Estimated for simple consumers.
	Estimated bool `json:"estimated"`
}

// StatsJSON renders the stats rollup. hitsAdopted is the count of adopted
// search hits reported back by clients — zero until adoption reporting
// reaches the server, and the estimate says so.
func StatsJSON(c serverstore.NetworkCounts, hitsAdopted int64, now time.Time) ([]byte, error) {
	doc := StatsDoc{
		SchemaVersion:      1,
		Day:                now.UTC().Format("2006-01-02"),
		GeneratedAt:        now.UTC().Format(time.RFC3339),
		Peers:              c.Peers,
		ProjectsMonth:      c.ProjectsMonth,
		Packages:           c.Packages,
		Symbols:            c.Symbols,
		Evidence:           c.Observations,
		VerifiedSamples:    c.VerifiedSamples,
		PostHitSuccessRate: 0,
		Estimated:          true,
		PostHitBuildPass: PlaceholderStat{
			Value: 0,
			Note:  "placeholder — no post-hit adoption data collected yet",
		},
		EstimatedReasoningAvoided: EstimatedStat{
			Estimated: true,
			Value:     hitsAdopted * 3,
			Formula:   "hitsAdopted * 3",
			Assumptions: []string{
				"each adopted hit avoids ~3 LLM reasoning calls (fixed v1 assumption)",
				"rework cost not yet measured, assumed 0",
			},
		},
	}
	return json.Marshal(doc)
}
