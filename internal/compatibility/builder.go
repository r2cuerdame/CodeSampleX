package compatibility

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
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
	// purls are the author-declared manifest packages. Resolver-established
	// versions live only on the individual receipts.
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

	allTargets, err := b.Store.ListSnapshotTargets(ctx)
	if err != nil {
		return fmt.Errorf("compatibility: list targets: %w", err)
	}
	targets := allTargets
	if affected != nil {
		// Receipt regressions compare adjacent measured versions, including
		// cross-major boundaries. A change to the old endpoint therefore also
		// invalidates snapshots of newer majors for the same package.
		affected = expandAffectedPackageMajors(affected, allTargets)
		targets = keepTargets(allTargets, affected)
	}
	samples, err := b.loadSamples(ctx)
	if err != nil {
		return err
	}
	if err := b.ensureReceiptPackages(ctx, samples); err != nil {
		return err
	}
	receiptRegressions := regressionsFromReceipts(samples)
	jdkBoundaries := jdkBoundariesFromReceipts(samples)

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

	// Index all known target versions by package and symbol so regression
	// detection (§10.3) can compare against V-1 across major version
	// boundaries on incremental passes.
	allVersionsOf := map[pkgKey]map[string][]string{}
	allPURLOf := map[pkgKey]map[string]map[string]string{}
	for _, at := range allTargets {
		p, perr := domain.ParsePURL(at.PURL)
		if perr != nil {
			continue
		}
		k := pkgKey{p.Ecosystem, p.Name}
		if allVersionsOf[k] == nil {
			allVersionsOf[k] = map[string][]string{}
			allPURLOf[k] = map[string]map[string]string{}
		}
		if allPURLOf[k][at.Symbol] == nil {
			allPURLOf[k][at.Symbol] = map[string]string{}
		}
		if _, ok := allPURLOf[k][at.Symbol][p.Version]; !ok {
			allVersionsOf[k][at.Symbol] = append(allVersionsOf[k][at.Symbol], p.Version)
			allPURLOf[k][at.Symbol][p.Version] = at.PURL
		}
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
		versions := allVersionsOf[k][t.Symbol]
		if prevVer, ok := PreviousVersion(versions, p.Version); ok {
			prevPURL := allPURLOf[k][t.Symbol][prevVer]
			prevRows := byPkg[k][t.Symbol][prevVer]
			if prevRows == nil {
				var eerr error
				prevRows, eerr = b.Store.EvidenceForTarget(ctx, prevPURL, t.Symbol)
				if eerr != nil {
					return fmt.Errorf("compatibility: evidence for %s %q: %w", prevPURL, t.Symbol, eerr)
				}
				if byPkg[k] == nil {
					byPkg[k] = symVer{}
					purlOf[k] = map[string]string{}
				}
				if byPkg[k][t.Symbol] == nil {
					byPkg[k][t.Symbol] = map[string][]serverstore.EvidenceRow{}
				}
				byPkg[k][t.Symbol][prevVer] = prevRows
				purlOf[k][prevVer] = prevPURL
			}
			regs = DetectRegressions(t.PURL, prevPURL, t.Symbol,
				rows, prevRows)
			regressionsByPkg[k] = append(regressionsByPkg[k], regs...)
		}
		regs = append(regs, receiptRegressions[receiptTarget{purl: p.String(), symbol: t.Symbol}]...)

		receipts := receiptsForTarget(samples, p, t.Symbol)
		snap := BuildSnapshot(t.PURL, t.Symbol, rows, receipts, regs, now)
		snap.JDKBoundaryCandidates = jdkBoundaries[receiptTarget{purl: p.String(), symbol: t.Symbol}]
		js, jerr := json.Marshal(snap)
		if jerr != nil {
			return fmt.Errorf("compatibility: marshal snapshot %s: %w", t.PURL, jerr)
		}
		if err := b.Store.PutSnapshot(ctx, t.PURL, t.Symbol, string(js)); err != nil {
			return fmt.Errorf("compatibility: put snapshot %s: %w", t.PURL, err)
		}
	}
	if err := b.retireSnapshots(ctx, allTargets, affected); err != nil {
		return err
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
	// A failure cluster is a per-PACKAGE aggregate, so rebuilding one needs
	// every version of that package — not only the versions this pass
	// happened to touch.
	//
	// UpsertFailureCluster replaces observation_count, versions and
	// env_summary outright. On an incremental pass byPkg held only the
	// dirty versions, so a cluster with 100 observations on 0.27.2 (all
	// windows) and 50 on 1.12.0 (all linux) was rewritten, after a change
	// to 1.12.0 alone, as 50 observations on linux with 0.27.2 dropped from
	// its version list. The stored cluster then understated the failure and
	// named the wrong version — and that is what the search shows a caller
	// as a known failure.
	clusterEvidence, err := b.evidenceForPackages(ctx, pkgKeys, allTargets, byPkg)
	if err != nil {
		return err
	}
	for _, k := range pkgKeys {
		evidenceByVersion := clusterEvidence[k]
		// Regressions recomputed over the SAME evidence the cluster is
		// built from, not over whatever versions this pass happened to
		// touch.
		//
		// regressionsByPkg holds only what was detected for the pass's own
		// targets, while the cluster is rebuilt across every version. So a
		// package that had a 1.11 -> 1.12 regression lost its flag on the
		// next incremental pass triggered by unrelated evidence for 0.27.2
		// -- and got it back on the following full pass. A flag that
		// flickers with the aggregation schedule is not a finding anyone
		// can act on, and this is the axis the bug/fix work is built on.
		regs := regressionsForPackage(k, evidenceByVersion)
		for _, cluster := range BuildClusters(k.ecosystem, k.name, evidenceByVersion, regs, now) {
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

// retireSnapshots deletes materialized rows whose final live source was
// removed. Receipt-only targets disappear on quarantine; without retirement
// their old PASS/regression JSON remained directly servable forever.
func (b *Builder) retireSnapshots(ctx context.Context, live []serverstore.SnapshotTarget, affected map[shardKey]bool) error {
	want := make(map[serverstore.SnapshotTarget]bool, len(live))
	for _, target := range live {
		want[target] = true
	}
	stored, err := b.Store.SnapshotKeys(ctx)
	if err != nil {
		return fmt.Errorf("compatibility: list snapshot keys: %w", err)
	}
	var stale []serverstore.SnapshotTarget
	for _, target := range stored {
		if want[target] {
			continue
		}
		if affected != nil {
			key, ok := keyFor(target.PURL)
			if !ok || !affected[key] {
				continue
			}
		}
		stale = append(stale, target)
	}
	if err := b.Store.DeleteSnapshots(ctx, stale); err != nil {
		return fmt.Errorf("compatibility: delete retired snapshots: %w", err)
	}
	return nil
}

// ensureReceiptPackages makes receipt-only versions reachable through the
// registry endpoints as well as through snapshots and shards. Observation
// ingest already creates package rows; exact v2 receipt targets may be the
// first time the network sees a release, so insert an UNKNOWN-publicness row
// once without refreshing the last-seen clock on every aggregation pass.
func (b *Builder) ensureReceiptPackages(ctx context.Context, samples []sampleData) error {
	seen := map[string]bool{}
	for _, sample := range samples {
		for _, receipt := range sample.receipts {
			for _, p := range receipt.ResolvedPackages {
				if seen[p.String()] {
					continue
				}
				seen[p.String()] = true
				if _, ok, err := b.Store.GetPackage(ctx, p.String()); err != nil {
					return fmt.Errorf("compatibility: get package %s: %w", p.String(), err)
				} else if ok {
					continue
				}
				if err := b.Store.UpsertPackage(ctx, serverstore.PackageRow{
					PURL: p.String(), Ecosystem: p.Ecosystem, Name: p.Name,
					Version: p.Version, Major: p.Major(), Publicness: "UNKNOWN",
				}); err != nil {
					return fmt.Errorf("compatibility: register receipt package %s: %w", p.String(), err)
				}
			}
		}
	}
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
	// Adoption reports were hardcoded to zero here, with a comment saying
	// they had not reached the server yet. They had not, because nothing
	// drained the client queue and no route existed to receive one; both
	// are connected now, so the number is read rather than assumed.
	adopt, err := b.Store.AdoptionSummary(ctx)
	if err != nil {
		return fmt.Errorf("compatibility: adoption summary: %w", err)
	}
	statsJSON, err := StatsJSON(counts, adopt, now)
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

// expandAffectedPackageMajors marks every known major of a dirty package.
// Receipt-derived regressions are attached to the newer endpoint, so only
// rebuilding the major named by a changed old receipt would leave the
// boundary stale until the next hourly full pass.
func expandAffectedPackageMajors(affected map[shardKey]bool, targets []serverstore.SnapshotTarget) map[shardKey]bool {
	dirtyPackages := map[string]bool{}
	for key := range affected {
		dirtyPackages[key.ecosystem+"\x00"+strings.ToLower(key.name)] = true
	}
	for _, target := range targets {
		key, ok := keyFor(target.PURL)
		if ok && dirtyPackages[key.ecosystem+"\x00"+strings.ToLower(key.name)] {
			affected[key] = true
		}
	}
	return affected
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

// loadSampleBatch bounds one page of the sample scan.
const loadSampleBatch = 1000

func (b *Builder) loadSamples(ctx context.Context) ([]sampleData, error) {
	// Every sample, in pages. One capped read of the newest 1000 meant that
	// past that many, the oldest samples stopped appearing in any shard at
	// all -- and shards are the only document clients ever read, so those
	// answers left the network silently, oldest first.
	var rows []serverstore.SampleRow
	for offset := 0; ; offset += loadSampleBatch {
		page, perr := b.Store.ListSamplesPage(ctx, loadSampleBatch, offset)
		if perr != nil {
			return nil, fmt.Errorf("compatibility: list samples: %w", perr)
		}
		rows = append(rows, page...)
		if len(page) < loadSampleBatch {
			break
		}
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

// receiptsForTarget collects only receipts that established this exact
// resolved purl. A v1 receipt, or a v2 receipt whose resolver could not
// establish a package list, is useful lifecycle evidence but is not version
// evidence and is deliberately absent here.
func receiptsForTarget(samples []sampleData, p domain.PURL, symbol string) []ReceiptInfo {
	var out []ReceiptInfo
	for _, sd := range samples {
		if symbol != "" && !sampleClaimsSymbol(sd, symbol) {
			continue
		}
		for _, rec := range sd.receipts {
			if receiptCoversPackage(rec, p) {
				out = append(out, rec)
			}
		}
	}
	return out
}

func receiptCoversPackage(rec ReceiptInfo, p domain.PURL) bool {
	for _, rp := range rec.ResolvedPackages {
		if rp.Ecosystem == p.Ecosystem && rp.Name == p.Name && rp.Version == p.Version {
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

// sampleShardPURLs is the union of what the author declared and what signed
// v2 receipts actually resolved. The former keeps the sample discoverable by
// its stated input; the latter creates the exact version shards that carry
// verified evidence. Neither silently substitutes for the other.
func sampleShardPURLs(sd sampleData) []domain.PURL {
	out := append([]domain.PURL(nil), sd.purls...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[p.String()] = true
	}
	for _, rec := range sd.receipts {
		for _, p := range rec.ResolvedPackages {
			if !seen[p.String()] {
				seen[p.String()] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func sampleVerifications(sd sampleData) []ShardVerification {
	// Count independent peers by exact package set once, then attach the
	// resulting level to each receipt-scoped execution. Environment and stage
	// verdict stay receipt-local; only the strength summary is shared.
	peersBySet := map[string]map[string]bool{}
	for _, rec := range sd.receipts {
		if len(rec.ResolvedPackages) == 0 || rec.ContractResult != string(domain.ResultPass) || rec.PeerID == "" {
			continue
		}
		key := receiptPackageSetKey(rec.ResolvedPackages)
		if peersBySet[key] == nil {
			peersBySet[key] = map[string]bool{}
		}
		peersBySet[key][rec.PeerID] = true
	}

	var out []ShardVerification
	for _, rec := range sd.receipts {
		if len(rec.ResolvedPackages) == 0 {
			continue
		}
		packages := make([]string, 0, len(rec.ResolvedPackages))
		for _, p := range rec.ResolvedPackages {
			packages = append(packages, p.String())
		}
		level := 0
		if rec.ContractResult == string(domain.ResultPass) {
			level = 3
			if len(peersBySet[receiptPackageSetKey(rec.ResolvedPackages)]) >= 2 {
				level = 4
			}
		}
		entry := ShardVerification{
			ResolvedPackages:  packages,
			Environment:       rec.Env,
			Stages:            rec.Stages,
			VerificationLevel: level,
		}
		if !rec.CreatedAt.IsZero() {
			entry.CreatedAt = rec.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		return string(domain.MustCanonicalJSON(out[i])) < string(domain.MustCanonicalJSON(out[j]))
	})
	return out
}

func receiptPackageSetKey(packages []domain.PURL) string {
	parts := make([]string, 0, len(packages))
	for _, p := range packages {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "\x00")
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
				if affected != nil && !affected[sk] {
					continue // clean key: its shard is already correct
				}
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
				stats, failures := SymbolStatsFromEvidence(rows)
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
		for _, p := range sampleShardPURLs(sd) {
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

	built := map[string]bool{}
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
		sampleSet := shardSamplesFor(samples, sk.ecosystem, sk.name, sk.major)
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
			entry.Samples = sampleSet.Samples
			entry.CanonicalCaseCountTotal = sampleSet.CanonicalCaseCountTotal
			entry.DistinctSubjectCountTotal = sampleSet.DistinctSubjectCountTotal
			pkgs = append(pkgs, *entry)
		}
		key := sk.ecosystem + "/" + sk.name + "/" + sk.major
		built[key] = true
		shardJSON, etag := BuildShard(key, pkgs, now)
		if err := b.Store.PutShard(ctx, key, etag, shardJSON); err != nil {
			return fmt.Errorf("compatibility: put shard %s: %w", key, err)
		}
	}
	// A key that WAS dirty and produced nothing has lost its last input --
	// the ordinary shape of a quarantine on a seeded package. Retiring only
	// on full passes left the withdrawn sample being served for up to an
	// hour, and the operator was told "the next aggregation pass rebuilds
	// the affected shards".
	if affected != nil {
		dirty := map[string]bool{}
		for sk := range affected {
			key := sk.ecosystem + "/" + sk.name + "/" + sk.major
			if !built[key] {
				dirty[key] = true
			}
		}
		return b.retireShardKeys(ctx, dirty, now)
	}
	return b.retireEmptyShards(ctx, built, now)
}

// retireEmptyShards empties shards nothing feeds any more.
//
// A shard was only ever written when something still fed it, so a key whose
// last live input disappeared was simply skipped — and the previous body
// stayed in the store, served to every client, forever. That is what
// `csx-server quarantine` promised to undo: it prints "hidden from search,
// shards and the explorer" and "the next aggregation pass rebuilds the
// affected shards", and for the ordinary case of a seeded package whose
// only sample is withdrawn, no pass ever rebuilt it — not the twelfth, not
// the hundredth, because a full pass rebuilds the keys it FINDS.
//
// Writing the empty shard is what actually reaches the clients: they hold
// the old body under its ETag and would keep getting 304 otherwise.
//
// Full passes only. On an incremental pass the built set is deliberately
// partial, and every untouched key would look retired.
func (b *Builder) retireEmptyShards(ctx context.Context, built map[string]bool, now time.Time) error {
	keys, err := b.Store.ShardKeys(ctx)
	if err != nil {
		return fmt.Errorf("compatibility: list shard keys: %w", err)
	}
	want := map[string]bool{}
	for _, key := range keys {
		if !built[key] {
			want[key] = true
		}
	}
	return b.retireShardKeys(ctx, want, now)
}

// retireShardKeys empties the named shards, skipping any that are already
// empty so an ETag is not churned for nothing.
func (b *Builder) retireShardKeys(ctx context.Context, keys map[string]bool, now time.Time) error {
	for key := range keys {
		_, prev, ok, gerr := b.Store.GetShard(ctx, key)
		if gerr != nil {
			return fmt.Errorf("compatibility: get shard %s: %w", key, gerr)
		}
		if !ok || isEmptyShard(prev) {
			continue // already empty: rewriting it would only churn ETags
		}
		shardJSON, etag := BuildShard(key, nil, now)
		if err := b.Store.PutShard(ctx, key, etag, shardJSON); err != nil {
			return fmt.Errorf("compatibility: retire shard %s: %w", key, err)
		}
	}
	return nil
}

// isEmptyShard reports whether a stored shard body already carries nothing.
func isEmptyShard(shardJSON string) bool {
	var doc struct {
		Packages []json.RawMessage `json:"packages"`
	}
	if json.Unmarshal([]byte(shardJSON), &doc) != nil {
		return false // unreadable: rewrite it rather than trust it
	}
	return len(doc.Packages) == 0
}

// shardSampleSet carries the bounded sample list plus counts computed from
// every matching sample before that cap is applied. The totals are therefore
// exact; they never present the visible top 20 as the package's full depth.
type shardSampleSet struct {
	Samples                   []ShardSample
	CanonicalCaseCountTotal   int
	DistinctSubjectCountTotal int
}

// shardSamplesFor renders the top samples covering (ecosystem, name, major),
// each with the contract stages its latest receipt actually reported. It also
// computes exact case and safe-symbol totals from the complete pre-cap input.
func shardSamplesFor(samples []sampleData, ecosystem, name, major string) shardSampleSet {
	var in []ShardSampleInput
	caseIDs := map[string]bool{}
	subjects := map[string]bool{}
	for _, sd := range samples {
		covered := false
		for _, sp := range sampleShardPURLs(sd) {
			if sp.Ecosystem == ecosystem && sp.Name == name && sp.Major() == major {
				covered = true
				break
			}
		}
		if !covered {
			continue
		}
		// CaseID is the canonical subject identity stored with current
		// manifests. Legacy manifests predate the derived field, so derive the
		// same identity from their canonical case content instead.
		caseID := sd.manifest.Case.CaseID
		if caseID == "" {
			caseID = sd.manifest.Case.ComputeID()
		}
		caseIDs[caseID] = true
		boundedSymbols, allSymbols, symbolsTruncated := symbolsForShard(sd.manifest.Symbols)
		for _, symbol := range allSymbols {
			subjects[symbol] = true
		}
		entry := ShardSample{
			SampleID: sd.row.SampleID,
			Goal:     sd.manifest.Case.Goal,
			Status:   sd.row.Status,
			License:  sd.row.License,
			// Keep the author's declaration and the resolver-established
			// versions side by side. The shard key is only reachability and is
			// never substituted for either one.
			Packages:         sd.manifest.Packages,
			Symbols:          boundedSymbols,
			SymbolsTruncated: symbolsTruncated,
			Verifications:    sampleVerifications(sd),
			Environment:      sd.manifest.Environment,
			Contract:         contractForShard(sd.manifest.Case.Contract),
			Believed:         sd.manifest.Case.Believed,
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
			Symbols:   allSymbols,
			HotScore:  sd.row.HotScore,
			CreatedAt: sd.row.CreatedAt,
		})
	}
	return shardSampleSet{
		Samples:                   TopShardSamples(in),
		CanonicalCaseCountTotal:   len(caseIDs),
		DistinctSubjectCountTotal: len(subjects),
	}
}

// createMatrixJobs opens only matrix work that the public container worker can
// prepare exactly. The old generic generator emitted partial OS/runtimeMajor
// wishes for environments no worker could prove; those rows are retired by
// migration 0010 and must not be recreated.
func (b *Builder) createMatrixJobs(ctx context.Context, samples []sampleData) error {
	for _, sd := range samples {
		if !isVerifiedStatus(sd.row.Status) || len(sd.receipts) == 0 {
			continue
		}
		if sd.manifest.Environment.Ecosystem != "maven" ||
			(sd.manifest.VerifierAdapter != "maven-java@1" && sd.manifest.VerifierAdapter != "gradle-java@1") ||
			sd.manifest.Environment.Runtime != "java" {
			continue
		}
		existing, err := b.Store.JobsForSample(ctx, sd.row.SampleID)
		if err != nil {
			return fmt.Errorf("compatibility: jobs for %s: %w", sd.row.SampleID, err)
		}
		existingRuntime := map[string]bool{}
		for _, j := range existing {
			if j.Reason != "matrix" {
				continue
			}
			want, wantErr := strictWorkerRequirements(j.WantEnvJSON)
			if wantErr == nil && exactJavaMatrixRequirements(want, sd.manifest) {
				existingRuntime[want.RuntimeVersion] = true
			}
		}
		for _, rec := range sd.receipts {
			if receiptCoversExactJavaMatrix(rec, sd.manifest) {
				existingRuntime[javaRuntimeLine(rec.Env.RuntimeVersion)] = true
			}
		}
		for _, runtimeVersion := range []string{"8", "11", "17", "21", "25"} {
			if existingRuntime[runtimeVersion] || !javaTargetFitsRuntime(sd.manifest.Environment.LanguageVersion, runtimeVersion) {
				continue
			}
			want := domain.WorkerRequirements{
				SandboxCapability: domain.CapContainerRun,
				VerifierAdapter:   sd.manifest.VerifierAdapter,
				// Only Linux publishes a Java image; without the pin the row
				// fills a Windows verifier's queue window with work it can
				// never run.
				OS:               "linux",
				Ecosystem:        "maven",
				Runtime:          "java",
				RuntimeVersion:   runtimeVersion,
				ExecutionContext: "java",
			}
			if _, err := b.Store.CreateJob(ctx, serverstore.JobRow{
				SampleID:    sd.row.SampleID,
				Reason:      "matrix",
				WantEnvJSON: string(domain.MustCanonicalJSON(want)),
				Status:      "open",
			}); err != nil {
				return fmt.Errorf("compatibility: create matrix job for %s: %w", sd.row.SampleID, err)
			}
		}
	}
	return nil
}

func strictWorkerRequirements(raw string) (domain.WorkerRequirements, error) {
	var want domain.WorkerRequirements
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&want); err != nil {
		return want, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return want, fmt.Errorf("wantEnv must contain exactly one object")
	}
	return want, nil
}

func receiptCoversExactJavaMatrix(rec ReceiptInfo, manifest domain.SampleManifest) bool {
	env := rec.Env.Normalize()
	if rec.SandboxCapability != domain.CapContainerRun || rec.VerifierAdapter != manifest.VerifierAdapter ||
		env.Ecosystem != "maven" || env.Runtime != "java" || env.ExecutionContext != "java" ||
		env.OS != "linux" || env.OSVersionBucket != "2023" || env.Distro != "amzn" || env.Libc != "glibc" ||
		env.Virtualization != "container" || env.ContainerRuntime != "docker" || env.Compiler != "javac" ||
		env.CompilerVersion != env.RuntimeVersion || !javaTargetFitsRuntime(manifest.Environment.LanguageVersion, env.RuntimeVersion) {
		return false
	}
	switch manifest.VerifierAdapter {
	case "maven-java@1":
		return env.PackageManager == "maven" && env.PackageManagerVersion == "3.9.11"
	case "gradle-java@1":
		want := "8.14.3"
		if env.RuntimeVersion == "25" {
			want = "9.7.0"
		}
		return env.PackageManager == "gradle" && env.PackageManagerVersion == want
	}
	return false
}

func exactJavaMatrixRequirements(want domain.WorkerRequirements, manifest domain.SampleManifest) bool {
	return want.SandboxCapability == domain.CapContainerRun &&
		want.VerifierAdapter == manifest.VerifierAdapter && want.Ecosystem == "maven" &&
		want.Runtime == "java" && want.ExecutionContext == "java" &&
		javaTargetFitsRuntime(manifest.Environment.LanguageVersion, want.RuntimeVersion) &&
		len(want.Frameworks) == 0 && want.BrowserFamily == "" && want.BrowserMajor == "" &&
		want.Engine == "" && want.EngineVersion == ""
}

func javaTargetFitsRuntime(target, runtimeVersion string) bool {
	line := map[string]int{"8": 8, "11": 11, "17": 17, "21": 21, "25": 25}
	runtime, ok := line[runtimeVersion]
	if !ok {
		return false
	}
	if target == "" {
		return true
	}
	return line[target] != 0 && line[target] <= runtime
}

func javaRuntimeLine(version string) string {
	if i := strings.IndexByte(version, '.'); i >= 0 {
		return version[:i]
	}
	return version
}

func isVerifiedStatus(status string) bool {
	switch status {
	case "CROSS_PASS", "MATRIX_PASS", "STABLE":
		return true
	}
	return false
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
	PostHitSuccessRate float64         `json:"postHitSuccessRate"`
	PostHitBuildPass   PlaceholderStat `json:"postHitBuildPass"`
	// PostHitBuildsReported is the DENOMINATOR: adoption reports that
	// carried a build outcome either way. It exists so a reader can tell a
	// measured 0% from an unmeasured one, which the rate alone cannot say.
	PostHitBuildsReported     int64         `json:"postHitBuildsReported"`
	EstimatedReasoningAvoided EstimatedStat `json:"estimatedReasoningAvoided"`
	// Estimated marks the whole document as containing estimated figures,
	// mirroring EstimatedReasoningAvoided.Estimated for simple consumers.
	Estimated bool `json:"estimated"`
}

// StatsJSON renders the stats rollup from the network counts and the
// adoption reports clients have sent back.
//
// postHitSuccessRate is builds that PASSED over builds that were reported
// either way. A report with no build attached is counted in neither: the
// agent did not measure, and folding "unknown" into either bucket would
// turn a gap in the data into a claim about it. With no reports at all the
// rate stays 0 and PostHitBuildPass keeps saying nothing has been
// collected — which is what the front page renders as an em dash rather
// than as "0%".
func StatsJSON(c serverstore.NetworkCounts, adopt serverstore.AdoptionCounts, now time.Time) ([]byte, error) {
	hitsAdopted := adopt.Applied
	rate := 0.0
	measured := adopt.BuildPass + adopt.BuildFail
	if measured > 0 {
		rate = float64(adopt.BuildPass) / float64(measured)
	}
	buildNote := "placeholder — no post-hit adoption data collected yet"
	if measured > 0 {
		buildNote = "builds reported after applying a sample"
	}
	doc := StatsDoc{
		SchemaVersion:         1,
		Day:                   now.UTC().Format("2006-01-02"),
		GeneratedAt:           now.UTC().Format(time.RFC3339),
		Peers:                 c.Peers,
		ProjectsMonth:         c.ProjectsMonth,
		Packages:              c.Packages,
		Symbols:               c.Symbols,
		Evidence:              c.Observations,
		VerifiedSamples:       c.VerifiedSamples,
		PostHitSuccessRate:    rate,
		PostHitBuildsReported: measured,
		Estimated:             true,
		PostHitBuildPass: PlaceholderStat{
			Value: float64(adopt.BuildPass),
			Note:  buildNote,
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

// evidenceForPackages gathers every version's evidence for each touched
// package, reusing what this pass already loaded and fetching only the
// versions it skipped.
//
// The cost is bounded by the number of versions of the packages that
// changed, which is what a correct cluster rebuild needs by definition. A
// full pass loads nothing extra, because byPkg already holds everything.
// regressionsForPackage applies the §10.3 rule across every version of one
// package, from the same evidence the failure clusters are built from.
//
// Detection during the snapshot loop is scoped to the pass's targets, which
// is correct for snapshots — they are per target — and wrong for clusters,
// which are per package. This is the per-package answer.
func regressionsForPackage(k pkgKey, byVersion map[string][]serverstore.EvidenceRow) []RegressionCandidate {
	// symbol -> version -> rows, and the purl each version was seen under.
	bySymbol := map[string]map[string][]serverstore.EvidenceRow{}
	purlOf := map[string]string{}
	for version, rows := range byVersion {
		for _, row := range rows {
			if row.Symbol == "" {
				continue // package-level evidence carries no symbol
			}
			if bySymbol[row.Symbol] == nil {
				bySymbol[row.Symbol] = map[string][]serverstore.EvidenceRow{}
			}
			bySymbol[row.Symbol][version] = append(bySymbol[row.Symbol][version], row)
			if purlOf[version] == "" {
				purlOf[version] = row.PURL
			}
		}
	}

	symbols := make([]string, 0, len(bySymbol))
	for sym := range bySymbol {
		symbols = append(symbols, sym)
	}
	sort.Strings(symbols)

	var out []RegressionCandidate
	for _, sym := range symbols {
		versions := make([]string, 0, len(bySymbol[sym]))
		for v := range bySymbol[sym] {
			versions = append(versions, v)
		}
		sort.Strings(versions) // deterministic; PreviousVersion orders properly
		for _, v := range versions {
			prev, ok := PreviousVersion(versions, v)
			if !ok {
				continue
			}
			out = append(out, DetectRegressions(
				purlOf[v], purlOf[prev], sym,
				bySymbol[sym][v], bySymbol[sym][prev])...)
		}
	}
	return out
}

// evidenceKey identifies one evidence_agg row. It is that table's unique key,
// which is what makes it safe to use as an identity: two reads returning the
// same key returned the same row, not two rows that happen to look alike.
type evidenceKey struct {
	purl, symbol, envHash, stage, result, errorFP string
}

func keyOf(row serverstore.EvidenceRow) evidenceKey {
	return evidenceKey{row.PURL, row.Symbol, row.EnvHash, row.Stage, row.Result, row.ErrorFingerprint}
}

func (b *Builder) evidenceForPackages(ctx context.Context, keys []pkgKey,
	allTargets []serverstore.SnapshotTarget, byPkg map[pkgKey]symVer,
) (map[pkgKey]map[string][]serverstore.EvidenceRow, error) {
	want := make(map[pkgKey]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	out := make(map[pkgKey]map[string][]serverstore.EvidenceRow, len(keys))
	// One symbol reaches the server under two spellings — the scanner's
	// qualified name on anonymous evidence, the author's bare one on a signed
	// receipt — and both become live snapshot targets. EvidenceForTarget
	// answers either with the same rows on purpose (symbolSpellings), which is
	// right per target and wrong here, where every target's rows are summed
	// into one per-package bucket: the shared rows would be counted once per
	// spelling. Production carried a failure cluster of 520 for 260 observed
	// failures because of it, and because a pass only reads the targets it is
	// rebuilding, the doubling came and went with the pass shape — so the
	// ledger the deploy transaction compares before and after moved with no
	// evidence gained or lost. Identity, not arrival, decides what is counted.
	seen := make(map[pkgKey]map[evidenceKey]bool, len(keys))
	add := func(k pkgKey, version string, rows []serverstore.EvidenceRow) {
		for _, row := range rows {
			if seen[k][keyOf(row)] {
				continue
			}
			seen[k][keyOf(row)] = true
			out[k][version] = append(out[k][version], row)
		}
	}
	for _, k := range keys {
		out[k] = map[string][]serverstore.EvidenceRow{}
		seen[k] = map[evidenceKey]bool{}
		for _, versions := range byPkg[k] {
			for version, rows := range versions {
				add(k, version, rows)
			}
		}
	}
	for _, t := range allTargets {
		p, err := domain.ParsePURL(t.PURL)
		if err != nil {
			continue
		}
		k := pkgKey{p.Ecosystem, p.Name}
		if !want[k] {
			continue
		}
		if _, loaded := byPkg[k][t.Symbol][p.Version]; loaded {
			continue // this pass already read it
		}
		rows, err := b.Store.EvidenceForTarget(ctx, t.PURL, t.Symbol)
		if err != nil {
			return nil, fmt.Errorf("compatibility: cluster evidence for %s %q: %w", t.PURL, t.Symbol, err)
		}
		add(k, p.Version, rows)
	}
	return out, nil
}
