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
	"github.com/r2cuerdame/codesamplex/internal/retrypolicy"
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
	// phaseNow and phaseLogf are private observability seams. They keep the
	// business clock above independent from monotonic elapsed-time tests.
	phaseNow  func() time.Time
	phaseLogf func(string, ...any)

	// lastRun and passes drive incremental rebuilds. RunLoop is the only
	// caller and is single-goroutine, so these need no locking.
	lastRun time.Time
	passes  int
	// A deterministic scoped-read failure must not prevent the scheduled
	// exhaustive repair merely because successful passes stop accumulating.
	fullRepairAt time.Time
	// A committed projection backfill invalidates a running builder's scope.
	// Advance this only after its required exhaustive repair succeeds.
	completedRepairGeneration uint64
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

	// snapshotWriteBatch bounds how many materialized documents share one
	// database checkout and transaction. Production 2026-09-07 rebuilt 4,255
	// targets one autocommit at a time while interactive reads were refused.
	// Sixty-four removes 98% of those checkouts/autocommit transactions without
	// replacing them with a whole-pass transaction that could hold a connection
	// for minutes.
	snapshotWriteBatch = 64
)

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now().UTC()
}

// RunLoop runs RunOnce immediately and spaces every later pass from the
// previous pass's completion. A failed pass gets at most five background
// retries at 1s/2s/4s/8s/16s plus jitter. Exhaustion is terminal for that
// retry series: the builder defers until its normal interval before opening a
// fresh series.
func (b *Builder) RunLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	runBuilderLoop(ctx, interval, b.RunOnce)
}

func runBuilderLoop(ctx context.Context, interval time.Duration, run func(context.Context) error) {
	runBuilderLoopWith(ctx, interval, run, waitBuilderDelay, nil)
}

func runBuilderLoopWith(
	ctx context.Context,
	interval time.Duration,
	run func(context.Context) error,
	wait func(context.Context, time.Duration) bool,
	draw func(time.Duration) time.Duration,
) {
	var series retrypolicy.Series
	retrying := false
	for {
		budget := serverstore.NewQueryBudget(serverstore.ClassBackground)
		if retrying {
			budget = serverstore.NewRetryQueryBudget(serverstore.ClassBackground)
		}
		err := run(serverstore.WithQueryBudget(ctx, budget))
		if ctx.Err() != nil {
			return
		}

		delay := interval
		if err == nil {
			series.Reset()
			retrying = false
		} else if retry, state := series.Failure(); state == retrypolicy.Waiting {
			delay, _ = retrypolicy.Delay(retry, draw)
			retrying = true
			log.Printf("compatibility: builder run failed: %v; background retry %d/%d in %s",
				err, retry, retrypolicy.MaxRetries, delay.Round(time.Millisecond))
		} else {
			log.Printf("compatibility: builder run failed after %d retries: %v; state=failed/deferred for %s",
				retrypolicy.MaxRetries, err, interval)
			retrying = false
		}
		if !wait(ctx, delay) {
			return
		}
		// Keep exhaustion terminal for the whole deferred window. Resetting in
		// the failure branch made the log claim failed/deferred while the state
		// was already Ready, allowing future loop changes to reinsert work
		// before the normal interval elapsed.
		if series.State() == retrypolicy.FailedDeferred {
			series.Reset()
		}
	}
}

func waitBuilderDelay(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// resumeWindow bounds how old a recorded pass may be and still be resumed
// from: past it, this deployment has been absent long enough that building
// from nothing is the honest answer.
//
// It was an hour first, on the reasoning that an hour is the periodic full
// pass. That would have made this whole change a no-op, and production said
// so. The stamp is written at the START of a pass -- refreshStats is handed
// the pass's own `now` -- and the full pass measured here ran from 02:49:25Z
// to 04:48:51Z. Its stamp was therefore 119 minutes old the moment it
// landed, so an hour would have refused every resume that follows a full
// pass, which is exactly the restart worth rescuing.
//
// A day is the bound that means what it says. Inside it, resuming asks
// ChangedSince for the same window the surviving process would have asked
// for on its next tick -- lastRun is passStart there too, so a process that
// just finished a two-hour pass already queries two hours of changes. The
// window is not protecting the incremental path from a long span; it is
// protecting it from a deployment that has been down long enough for
// "everything that changed since" to stop resembling a delta at all.
const resumeWindow = 24 * time.Hour

// resumeFromLastCompletedPass seeds lastRun from what the previous process
// durably recorded, so a restart does not force the most expensive pass.
//
// A completed pass writes generatedAt into stats, which already makes that
// timestamp the "last run" marker -- it was simply never read back, so every
// restart began from zero and rebuilt the whole corpus. Measured on
// production 2026-09-02: a container recreated four minutes after a pass
// completed took a full pass anyway, and had not finished it 104 minutes
// later, during which the site's clock could not move at all (RunLoop calls
// the first pass synchronously, and stats are written at the end of one).
//
// Best effort in both directions. A store that cannot answer, a stamp that
// will not parse, one from the future, and one older than resumeWindow all
// leave lastRun zero, which is the previous behaviour: take the full pass.
func (b *Builder) resumeFromLastCompletedPass(ctx context.Context, now time.Time) {
	if !b.lastRun.IsZero() {
		return // already running; this is only about the first pass
	}
	js, ok, err := b.Store.GetLatestStats(ctx)
	if err != nil || !ok {
		return
	}
	var doc struct {
		GeneratedAt           string `json:"generatedAt"`
		BuilderRepairRequired bool   `json:"builderRepairRequired"`
	}
	if json.Unmarshal([]byte(js), &doc) != nil || doc.GeneratedAt == "" || doc.BuilderRepairRequired {
		return
	}
	stamp, perr := time.Parse(time.RFC3339, doc.GeneratedAt)
	if perr != nil || stamp.After(now) || now.Sub(stamp) > resumeWindow {
		return
	}
	b.lastRun = stamp

	// passes must move off zero too, or the periodic full-pass rule
	// (passes%fullPassEvery == 0) fires on this very pass and undoes the
	// resume -- the seeded lastRun would be read, and a full pass taken
	// anyway. One is simply "not the first", which is what resuming means.
	b.passes = 1
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

// PostgreSQL supplies an indexed incremental source path. Alternate stores
// retain the exhaustive implementation, which also remains the full/hourly
// repair path and the independent parity oracle in tests.
type builderRepairGenerationStore interface {
	BuilderRepairGeneration() uint64
}

type incrementalSourceStore interface {
	BuilderChangesSince(context.Context, time.Time) (serverstore.Changes, error)
	ListBuilderSnapshotTargets(context.Context, []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, serverstore.BuilderReadMetrics, error)
	ListBuilderSamplesPage(context.Context, []serverstore.BuilderPackage, int, int) ([]serverstore.SampleRow, error)
	BuilderSnapshotKeys(context.Context, []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, error)
}

func affectedPackages(affected map[shardKey]bool) []serverstore.BuilderPackage {
	seen := map[serverstore.BuilderPackage]bool{}
	for k := range affected {
		seen[serverstore.BuilderPackage{Ecosystem: k.ecosystem, Name: strings.ToLower(k.name)}] = true
	}
	out := make([]serverstore.BuilderPackage, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out
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
func (b *Builder) RunOnce(ctx context.Context) (runErr error) {
	phases := b.newPhaseRecorder(ctx)
	ctx = withBuilderPhaseRecorder(ctx, phases)
	defer func() { phases.finish(runErr) }()

	started := time.Now()
	now := b.now()
	passStart := now
	resumeReads := int64(0)
	if b.lastRun.IsZero() {
		resumeReads = 1
	}
	phase := phases.begin(phaseResume)
	b.resumeFromLastCompletedPass(ctx, now)
	phase.end(nil, knownCalls(resumeReads))
	phases.close(phaseResume)
	if b.fullRepairAt.IsZero() {
		b.fullRepairAt = now.Add(time.Hour)
	}
	var repairGeneration uint64
	if store, ok := b.Store.(builderRepairGenerationStore); ok {
		repairGeneration = store.BuilderRepairGeneration()
	}
	full := b.lastRun.IsZero() || b.passes%fullPassEvery == 0 || !now.Before(b.fullRepairAt) ||
		repairGeneration != b.completedRepairGeneration
	changeSince := b.lastRun.Add(-changeOverlap)
	log.Printf("compatibility: builder pass start full=%t since=%s", full, changeSince.UTC().Format(time.RFC3339Nano))

	// affected limits the rebuild to shard keys touched since the last
	// pass; nil means "everything", which is what a full pass wants.
	var affected map[shardKey]bool
	scoped, hasScoped := b.Store.(incrementalSourceStore)
	if !full {
		phase = phases.begin(phaseChanges)
		var changes serverstore.Changes
		var cerr error
		if hasScoped {
			changes, cerr = scoped.BuilderChangesSince(ctx, changeSince)
		} else {
			changes, cerr = b.Store.ChangedSince(ctx, changeSince)
		}
		phase.end(cerr, builderPhaseCounters{
			logicalCalls: 1, callsKnown: true,
			items: int64(len(changes.Targets) + len(changes.SamplePURLs)),
		})
		phases.close(phaseChanges)
		if cerr != nil {
			return fmt.Errorf("compatibility: changes since %s: %w", b.lastRun, cerr)
		}
		if changes.Empty() {
			// Nothing moved. Stats still refresh — they are one query and
			// they carry the clock the website displays.
			phase = phases.begin(phaseRefreshStats)
			err := b.refreshStats(ctx, now)
			phase.end(err, builderPhaseCounters{callsKnown: true})
			phases.close(phaseRefreshStats)
			if err != nil {
				return err
			}
			b.passes++
			b.lastRun = passStart
			log.Printf("compatibility: builder pass complete full=false since=%s targets=0 packages=0 clusters=0 cluster_read=0s cluster_calculate=0s cluster_write=0s total=%s",
				changeSince.UTC().Format(time.RFC3339Nano), time.Since(started))
			return nil
		}
		affected = affectedKeys(changes)
	}

	phase = phases.begin(phaseListTargets)
	var allTargets []serverstore.SnapshotTarget
	var err error
	if affected != nil && hasScoped {
		projectionPhase := phases.begin(phaseTargetProjectionRead)
		var read serverstore.BuilderReadMetrics
		allTargets, read, err = scoped.ListBuilderSnapshotTargets(ctx, affectedPackages(affected))
		projectionPhase.end(err, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: read.Rows, bytes: read.Bytes})
		phases.close(phaseTargetProjectionRead)
	} else {
		allTargets, err = b.Store.ListSnapshotTargets(ctx)
	}
	phase.end(err, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(allTargets))})
	phases.close(phaseListTargets)
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
	phase = phases.begin(phaseLoadSamples)
	samples, err := b.loadSamplesForPackages(ctx, affected)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseSamplePageRead)
	phases.close(phaseReceiptPageRead)
	phases.close(phaseDecode)
	phases.close(phaseLoadSamples)
	if err != nil {
		return err
	}
	if affected != nil && hasScoped {
		// A declaration can create a source-only shard with no target. Include
		// all its majors too when rebuilding the selected package histories.
		var sampleTargets []serverstore.SnapshotTarget
		for _, sd := range samples {
			for _, p := range sampleShardPURLs(sd) {
				sampleTargets = append(sampleTargets, serverstore.SnapshotTarget{PURL: p.String()})
			}
		}
		affected = expandAffectedPackageMajors(affected, sampleTargets)
	}
	phase = phases.begin(phaseEnsureReceiptPackages)
	err = b.ensureReceiptPackages(ctx, samples)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseEnsureReceiptPackages)
	if err != nil {
		return err
	}
	phase = phases.begin(phaseReceiptDerivedCalculation)
	receiptRegressions := regressionsFromReceipts(samples)
	jdkBoundaries := jdkBoundariesFromReceipts(samples)
	phase.end(nil, builderPhaseCounters{items: int64(len(samples)), callsKnown: true})
	phases.close(phaseReceiptDerivedCalculation)

	// Evidence indexed by package → symbol → version → rows.
	byPkg := map[pkgKey]symVer{}
	purlOf := map[pkgKey]map[string]string{} // version → purl string
	for _, t := range targets {
		p, perr := domain.ParsePURL(t.PURL)
		if perr != nil {
			continue
		}
		phase = phases.begin(phaseTargetEvidence)
		rows, eerr := b.Store.EvidenceForTarget(ctx, t.PURL, t.Symbol)
		phase.end(eerr, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(rows))})
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
	// PostgreSQL can pipeline one bounded chunk; fakes and alternate stores
	// keep the row-at-a-time contract through the fallback below.
	snapshotRows := make([]serverstore.SnapshotRow, 0, snapshotWriteBatch)
	flushSnapshots := func() error {
		if len(snapshotRows) == 0 {
			return nil
		}
		if batchStore, ok := b.Store.(interface {
			PutSnapshots(context.Context, []serverstore.SnapshotRow) error
		}); ok {
			phase := phases.begin(phaseSnapshotWrite)
			err := batchStore.PutSnapshots(ctx, snapshotRows)
			phase.end(err, builderPhaseCounters{
				logicalCalls: 1, callsKnown: true, items: int64(len(snapshotRows)),
				bytes: snapshotRowsBytes(snapshotRows),
			})
			if err != nil {
				return err
			}
		} else {
			for _, row := range snapshotRows {
				phase := phases.begin(phaseSnapshotWrite)
				err := b.Store.PutSnapshot(ctx, row.PURL, row.Symbol, row.SnapshotJSON)
				phase.end(err, builderPhaseCounters{
					logicalCalls: 1, callsKnown: true, items: 1, bytes: int64(len(row.SnapshotJSON)),
				})
				if err != nil {
					return err
				}
			}
		}
		snapshotRows = snapshotRows[:0]
		return nil
	}
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
				phase = phases.begin(phaseTargetEvidence)
				prevRows, eerr = b.Store.EvidenceForTarget(ctx, prevPURL, t.Symbol)
				phase.end(eerr, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(prevRows))})
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

		phase = phases.begin(phaseSnapshotCalculate)
		receipts := receiptsForTarget(samples, p, t.Symbol)
		snap := BuildSnapshot(t.PURL, t.Symbol, rows, receipts, regs, now)
		snap.JDKBoundaryCandidates = jdkBoundaries[receiptTarget{purl: p.String(), symbol: t.Symbol}]
		js, jerr := json.Marshal(snap)
		phase.end(jerr, builderPhaseCounters{items: 1, bytes: int64(len(js)), callsKnown: true})
		if jerr != nil {
			return fmt.Errorf("compatibility: marshal snapshot %s: %w", t.PURL, jerr)
		}
		snapshotRows = append(snapshotRows, serverstore.SnapshotRow{
			PURL: t.PURL, Symbol: t.Symbol, SnapshotJSON: string(js),
		})
		if len(snapshotRows) == snapshotWriteBatch {
			if err := flushSnapshots(); err != nil {
				return fmt.Errorf("compatibility: put snapshot batch ending %s: %w", t.PURL, err)
			}
		}
	}
	phases.completeEmpty(phaseTargetEvidence)
	phases.completeEmpty(phaseSnapshotCalculate)
	phases.close(phaseTargetEvidence)
	phases.close(phaseSnapshotCalculate)
	if err := flushSnapshots(); err != nil {
		return fmt.Errorf("compatibility: put final snapshot batch: %w", err)
	}
	phases.completeEmpty(phaseSnapshotWrite)
	phases.close(phaseSnapshotWrite)
	phase = phases.begin(phaseSnapshotRetire)
	err = b.retireSnapshots(ctx, allTargets, affected)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseSnapshotRetire)
	if err != nil {
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
	type packageTiming struct {
		key                    pkgKey
		read, calculate, write time.Duration
		clusters               int
	}
	var clusterRead, clusterCalculate, clusterWrite time.Duration
	var clusterCount int
	var slowest packageTiming
	targetsByPkg := map[pkgKey][]parsedTarget{}
	for _, t := range allTargets {
		p, perr := domain.ParsePURL(t.PURL)
		if perr != nil {
			continue
		}
		pk := pkgKey{p.Ecosystem, p.Name}
		targetsByPkg[pk] = append(targetsByPkg[pk], parsedTarget{target: t, version: p.Version})
	}

	for _, k := range pkgKeys {
		if ctx.Err() != nil {
			phase = phases.begin(phaseClusterRead)
			phase.end(ctx.Err(), knownCalls(0))
			return ctx.Err()
		}
		pkgTiming := packageTiming{key: k}
		phaseStart := time.Now()
		phase = phases.begin(phaseClusterRead)
		evidenceByVersion, err := b.evidenceForPackage(ctx, k, targetsByPkg[k], byPkg)
		phase.end(err, builderPhaseCounters{callsKnown: true})
		pkgTiming.read = time.Since(phaseStart)
		clusterRead += pkgTiming.read
		if err != nil {
			return err
		}
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
		phaseStart = time.Now()
		phase = phases.begin(phaseClusterCalculate)
		regs := regressionsForPackage(k, evidenceByVersion)
		clusters := BuildClusters(k.ecosystem, k.name, evidenceByVersion, regs, now)
		phase.end(nil, builderPhaseCounters{items: int64(len(clusters)), callsKnown: true})
		pkgTiming.calculate = time.Since(phaseStart)
		clusterCalculate += pkgTiming.calculate
		pkgTiming.clusters = len(clusters)
		clusterCount += len(clusters)

		phaseStart = time.Now()
		if batchStore, ok := b.Store.(interface {
			UpsertFailureClusters(context.Context, []serverstore.ClusterRow) error
		}); ok {
			phase = phases.begin(phaseClusterWrite)
			err := batchStore.UpsertFailureClusters(ctx, clusters)
			phase.end(err, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(clusters))})
			if err != nil {
				return fmt.Errorf("compatibility: upsert clusters %s/%s: %w", k.ecosystem, k.name, err)
			}
		} else {
			for _, cluster := range clusters {
				phase = phases.begin(phaseClusterWrite)
				err := b.Store.UpsertFailureCluster(ctx, cluster)
				phase.end(err, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: 1})
				if err != nil {
					return fmt.Errorf("compatibility: upsert cluster %s/%s: %w", k.ecosystem, k.name, err)
				}
			}
		}
		pkgTiming.write = time.Since(phaseStart)
		clusterWrite += pkgTiming.write
		if pkgTiming.read+pkgTiming.calculate+pkgTiming.write > slowest.read+slowest.calculate+slowest.write {
			slowest = pkgTiming
		}
		phases.progress(phaseClusterRead)
	}
	phases.completeEmpty(phaseClusterRead)
	phases.completeEmpty(phaseClusterCalculate)
	phases.completeEmpty(phaseClusterWrite)
	phases.close(phaseClusterRead)
	phases.close(phaseClusterCalculate)
	phases.close(phaseClusterWrite)

	// C6 shards per (ecosystem, name, major).
	phase = phases.begin(phaseShards)
	err = b.regenerateShards(ctx, byPkg, purlOf, samples, affected, now)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseShards)
	if err != nil {
		return err
	}

	// Matrix jobs for CROSS_PASS+ samples (§10.2 one-variable-changed).
	phase = phases.begin(phaseMatrixJobs)
	err = b.createMatrixJobs(ctx, samples)
	phase.end(err, builderPhaseCounters{callsKnown: true, items: int64(len(samples))})
	phases.completeEmpty(phaseMatrixJobHistoryRead)
	phases.close(phaseMatrixJobHistoryRead)
	phases.close(phaseMatrixJobs)
	if err != nil {
		return err
	}

	// Verification work for coordinates whose DEPENDENCY axis is open (#87, #69).
	phase = phases.begin(phaseDependencyAxis)
	err = b.createDependencyAxisJobs(ctx)
	phase.end(err, builderPhaseCounters{})
	phases.close(phaseDependencyAxis)
	if err != nil {
		return err
	}

	phase = phases.begin(phaseRefreshStats)
	err = b.refreshStats(ctx, now)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseRefreshStats)
	if err != nil {
		return err
	}
	b.passes++
	b.lastRun = passStart
	if full {
		// Schedule from completion: a slow repair must not make the next
		// ordinary tick immediately repeat the whole corpus again.
		b.fullRepairAt = b.now().Add(time.Hour)
		b.completedRepairGeneration = repairGeneration
	}
	log.Printf("compatibility: builder pass complete full=%t since=%s targets=%d packages=%d clusters=%d cluster_read=%s cluster_calculate=%s cluster_write=%s slowest_package=%s/%s slowest_read=%s slowest_calculate=%s slowest_write=%s slowest_clusters=%d total=%s",
		full, changeSince.UTC().Format(time.RFC3339Nano), len(targets), len(pkgKeys), clusterCount,
		clusterRead, clusterCalculate, clusterWrite, slowest.key.ecosystem, slowest.key.name,
		slowest.read, slowest.calculate, slowest.write, slowest.clusters, time.Since(started))
	return nil
}

// retireSnapshots deletes materialized rows whose final live source was
// removed. Receipt-only targets disappear on quarantine; without retirement
// their old PASS/regression JSON remained directly servable forever.
func (b *Builder) retireSnapshots(ctx context.Context, live []serverstore.SnapshotTarget, affected map[shardKey]bool) error {
	phases := builderPhases(ctx)
	want := make(map[serverstore.SnapshotTarget]bool, len(live))
	for _, target := range live {
		want[target] = true
	}
	var stored []serverstore.SnapshotTarget
	var err error
	scoped, hasScoped := b.Store.(incrementalSourceStore)
	if affected != nil && hasScoped {
		stored, err = scoped.BuilderSnapshotKeys(ctx, affectedPackages(affected))
		// Retired majors are absent from live targets but must also have their
		// stale snapshots and shards withdrawn in this incremental pass.
		affected = expandAffectedPackageMajors(affected, stored)
	} else {
		stored, err = b.Store.SnapshotKeys(ctx)
	}
	phases.add(phaseSnapshotRetire, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(stored))})
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
	err = b.Store.DeleteSnapshots(ctx, stale)
	phases.add(phaseSnapshotRetire, knownCalls(1))
	if err != nil {
		return fmt.Errorf("compatibility: delete retired snapshots: %w", err)
	}
	return nil
}

// packageProbeBatch bounds how many purls share one existence query. The
// whole corpus in a single array parameter would trade a long queue of small
// reads for one unbounded one.
const packageProbeBatch = 1000

type packageProbeStore interface {
	ExistingPackagePURLs(context.Context, []string) (map[string]bool, error)
}

// ensureReceiptPackages makes receipt-only versions reachable through the
// registry endpoints as well as through snapshots and shards. Observation
// ingest already creates package rows; exact v2 receipt targets may be the
// first time the network sees a release, so insert an UNKNOWN-publicness row
// once without refreshing the last-seen clock on every aggregation pass.
//
// Which of them are already known is asked in bounded batches. Asking one
// package at a time was a read per package in the WHOLE corpus on every pass,
// incremental ones included: production v0.1.147 reported pool_busy=143 and a
// 16.357s maximum wait for a connection while a pass with one dirty package
// did exactly this. The set registered is unchanged -- membership is all that
// moved into the store.
func (b *Builder) ensureReceiptPackages(ctx context.Context, samples []sampleData) error {
	seen := map[string]bool{}
	// First-seen order, not map order, so registration writes the same rows
	// in the same sequence as the read-per-package version did.
	var resolved []domain.PURL
	for _, sample := range samples {
		for _, receipt := range sample.receipts {
			for _, p := range receipt.ResolvedPackages {
				if seen[p.String()] {
					continue
				}
				seen[p.String()] = true
				resolved = append(resolved, p)
			}
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	phases := builderPhases(ctx)
	phases.add(phaseEnsureReceiptPackages, builderPhaseCounters{items: int64(len(resolved)), callsKnown: true})
	known, err := b.knownPackages(ctx, resolved)
	if err != nil {
		return err
	}
	for _, p := range resolved {
		if known[p.String()] {
			continue
		}
		err := b.Store.UpsertPackage(ctx, serverstore.PackageRow{
			PURL: p.String(), Ecosystem: p.Ecosystem, Name: p.Name,
			Version: p.Version, Major: p.Major(), Publicness: "UNKNOWN",
		})
		phases.add(phaseEnsureReceiptPackages, knownCalls(1))
		if err != nil {
			return fmt.Errorf("compatibility: register receipt package %s: %w", p.String(), err)
		}
	}
	return nil
}

// knownPackages reports which of these purls the registry already holds.
// PostgreSQL answers a bounded page at a time; alternate stores keep the
// original read-per-package contract through the fallback below.
func (b *Builder) knownPackages(ctx context.Context, resolved []domain.PURL) (map[string]bool, error) {
	known := make(map[string]bool, len(resolved))
	phases := builderPhases(ctx)
	probe, ok := b.Store.(packageProbeStore)
	if !ok {
		for _, p := range resolved {
			_, found, err := b.Store.GetPackage(ctx, p.String())
			phases.add(phaseEnsureReceiptPackages, knownCalls(1))
			if err != nil {
				return nil, fmt.Errorf("compatibility: get package %s: %w", p.String(), err)
			} else if found {
				known[p.String()] = true
			}
		}
		return known, nil
	}
	for start := 0; start < len(resolved); start += packageProbeBatch {
		end := start + packageProbeBatch
		if end > len(resolved) {
			end = len(resolved)
		}
		purls := make([]string, 0, end-start)
		for _, p := range resolved[start:end] {
			purls = append(purls, p.String())
		}
		page, err := probe.ExistingPackagePURLs(ctx, purls)
		phases.add(phaseEnsureReceiptPackages, builderPhaseCounters{logicalCalls: 1, callsKnown: true, pages: 1})
		if err != nil {
			return nil, fmt.Errorf("compatibility: existing packages: %w", err)
		}
		for purl, found := range page {
			if found {
				known[purl] = true
			}
		}
	}
	return known, nil
}

// refreshStats writes the daily rollup. It runs on every pass, including
// passes with nothing else to do: it is a single query, and it is what the
// website's counters and generatedAt timestamp come from.
func (b *Builder) refreshStats(ctx context.Context, now time.Time) error {
	phases := builderPhases(ctx)
	counts, err := b.Store.NetworkCounts(ctx, now)
	phases.add(phaseRefreshStats, knownCalls(1))
	if err != nil {
		return fmt.Errorf("compatibility: network counts: %w", err)
	}
	// Adoption reports were hardcoded to zero here, with a comment saying
	// they had not reached the server yet. They had not, because nothing
	// drained the client queue and no route existed to receive one; both
	// are connected now, so the number is read rather than assumed.
	adopt, err := b.Store.AdoptionSummary(ctx)
	phases.add(phaseRefreshStats, knownCalls(1))
	if err != nil {
		return fmt.Errorf("compatibility: adoption summary: %w", err)
	}
	statsJSON, err := StatsJSON(counts, adopt, now)
	if err != nil {
		return err
	}
	err = b.Store.SetStatsDaily(ctx, now.Format("2006-01-02"), string(statsJSON))
	phases.add(phaseRefreshStats, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: 1, bytes: int64(len(statsJSON))})
	if err != nil {
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

type receiptPageStore interface {
	ReceiptsForSamples(context.Context, []string) (map[string][]serverstore.ReceiptRow, error)
}

func (b *Builder) loadSamples(ctx context.Context) ([]sampleData, error) {
	return b.loadSamplesForPackages(ctx, nil)
}

func (b *Builder) loadSamplesForPackages(ctx context.Context, affected map[shardKey]bool) ([]sampleData, error) {
	// Process the corpus one sample page at a time. PostgreSQL can fetch the
	// receipt history for that page in one checkout; alternate stores keep the
	// original per-sample contract through the fallback below.
	var out []sampleData
	bulk, hasBulkReceipts := b.Store.(receiptPageStore)
	phases := builderPhases(ctx)
	scoped, hasScoped := b.Store.(incrementalSourceStore)
	for offset := 0; ; offset += loadSampleBatch {
		readPhase := phases.begin(phaseSamplePageRead)
		var page []serverstore.SampleRow
		var perr error
		if affected != nil && hasScoped {
			page, perr = scoped.ListBuilderSamplesPage(ctx, affectedPackages(affected), loadSampleBatch, offset)
		} else {
			page, perr = b.Store.ListSamplesPage(ctx, loadSampleBatch, offset)
		}
		pageCounters := builderPhaseCounters{
			logicalCalls: 1, callsKnown: true, pages: 1,
			items: int64(len(page)), bytes: sampleRowsBytes(page),
		}
		readPhase.end(perr, pageCounters)
		phases.add(phaseLoadSamples, pageCounters.withoutItems())
		if perr != nil {
			return nil, fmt.Errorf("compatibility: list samples: %w", perr)
		}

		decodePhase := phases.begin(phaseDecode)
		parsed := make([]sampleData, 0, len(page))
		sampleIDs := make([]string, 0, len(page))
		for _, row := range page {
			var manifest domain.SampleManifest
			if json.Unmarshal([]byte(row.ManifestJSON), &manifest) != nil {
				continue
			}
			sd := sampleData{row: row, manifest: manifest}
			for _, ps := range manifest.Packages {
				if p, err := domain.ParsePURL(ps); err == nil {
					sd.purls = append(sd.purls, p)
				}
			}
			parsed = append(parsed, sd)
			sampleIDs = append(sampleIDs, row.SampleID)
		}
		decodeCounters := builderPhaseCounters{
			logicalCalls: 0, callsKnown: true, items: int64(len(parsed)), bytes: sampleRowsBytes(page),
		}
		decodePhase.end(nil, decodeCounters)

		var receiptPages map[string][]serverstore.ReceiptRow
		if hasBulkReceipts && len(sampleIDs) > 0 {
			var err error
			receiptPhase := phases.begin(phaseReceiptPageRead)
			receiptPages, err = bulk.ReceiptsForSamples(ctx, sampleIDs)
			items, bytes := receiptPagesMetrics(receiptPages)
			receiptCounters := builderPhaseCounters{
				logicalCalls: 1, callsKnown: true, pages: 1, items: items, bytes: bytes,
			}
			receiptPhase.end(err, receiptCounters)
			phases.add(phaseLoadSamples, receiptCounters.withoutItems())
			if err != nil {
				return nil, fmt.Errorf("compatibility: receipts for sample page: %w", err)
			}
		}
		for i := range parsed {
			receiptRows := receiptPages[parsed[i].row.SampleID]
			if !hasBulkReceipts {
				var err error
				receiptPhase := phases.begin(phaseReceiptPageRead)
				receiptRows, err = b.Store.ReceiptsForSample(ctx, parsed[i].row.SampleID)
				items, bytes := receiptRowsMetrics(receiptRows)
				receiptCounters := builderPhaseCounters{
					logicalCalls: 1, callsKnown: true, pages: 1, items: items, bytes: bytes,
				}
				receiptPhase.end(err, receiptCounters)
				phases.add(phaseLoadSamples, receiptCounters.withoutItems())
				if err != nil {
					return nil, fmt.Errorf("compatibility: receipts for %s: %w", parsed[i].row.SampleID, err)
				}
			}
			decodePhase = phases.begin(phaseDecode)
			var decodedReceipts int64
			var decodedReceiptBytes int64
			for _, rr := range receiptRows {
				decodedReceiptBytes += int64(len(rr.ReceiptJSON))
				if info, ok := ParseReceiptRow(rr); ok {
					parsed[i].receipts = append(parsed[i].receipts, info)
					decodedReceipts++
				}
			}
			decodePhase.end(nil, builderPhaseCounters{callsKnown: true, items: decodedReceipts, bytes: decodedReceiptBytes})
		}
		out = append(out, parsed...)
		phases.progress(phaseLoadSamples)
		if len(page) < loadSampleBatch {
			break
		}
	}
	phases.completeEmpty(phaseReceiptPageRead)
	phases.close(phaseSamplePageRead)
	phases.close(phaseReceiptPageRead)
	phases.close(phaseDecode)
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

	phases := builderPhases(ctx)
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
		err := b.Store.PutShard(ctx, key, etag, shardJSON)
		phases.add(phaseShards, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: 1, bytes: int64(len(shardJSON))})
		if err != nil {
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
	builderPhases(ctx).add(phaseShards, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(keys))})
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
	phases := builderPhases(ctx)
	for key := range keys {
		_, prev, ok, gerr := b.Store.GetShard(ctx, key)
		phases.add(phaseShards, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: 1, bytes: int64(len(prev))})
		if gerr != nil {
			return fmt.Errorf("compatibility: get shard %s: %w", key, gerr)
		}
		if !ok || isEmptyShard(prev) {
			continue // already empty: rewriting it would only churn ETags
		}
		shardJSON, etag := BuildShard(key, nil, now)
		err := b.Store.PutShard(ctx, key, etag, shardJSON)
		phases.add(phaseShards, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: 1, bytes: int64(len(shardJSON))})
		if err != nil {
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
	phases := builderPhases(ctx)
	// Decide eligibility first, so the job history is asked for once for the
	// samples that can actually open matrix work rather than once per sample.
	eligible := make([]*sampleData, 0, len(samples))
	for i := range samples {
		sd := &samples[i]
		if !isVerifiedStatus(sd.row.Status) || len(sd.receipts) == 0 {
			continue
		}
		if sd.manifest.Environment.Ecosystem != "maven" ||
			(sd.manifest.VerifierAdapter != "maven-java@1" && sd.manifest.VerifierAdapter != "gradle-java@1") ||
			sd.manifest.Environment.Runtime != "java" {
			continue
		}
		eligible = append(eligible, sd)
	}
	if len(eligible) == 0 {
		return nil
	}
	jobs, err := b.jobsForSamples(ctx, eligible)
	if err != nil {
		return err
	}
	for _, sd := range eligible {
		existing := jobs[sd.row.SampleID]
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
			wantJSON := string(domain.MustCanonicalJSON(want))
			_, err := b.Store.CreateJob(ctx, serverstore.JobRow{
				SampleID:    sd.row.SampleID,
				Reason:      "matrix",
				WantEnvJSON: wantJSON,
				Status:      "open",
			})
			phases.add(phaseMatrixJobs, builderPhaseCounters{logicalCalls: 1, callsKnown: true, bytes: int64(len(wantJSON))})
			if err != nil {
				return fmt.Errorf("compatibility: create matrix job for %s: %w", sd.row.SampleID, err)
			}
		}
	}
	return nil
}

// jobProbeBatch bounds how many samples share one job-history query, for the
// same reason packageProbeBatch bounds the package existence query.
const jobProbeBatch = 1000

type jobPageStore interface {
	JobsForSamples(context.Context, []string) (map[string][]serverstore.JobRow, error)
}

// jobsForSamples reads the verification-job history of the eligible samples.
// PostgreSQL answers a bounded page in one checkout; alternate stores keep
// the original read-per-sample contract through the fallback below.
//
// Every job of every eligible sample is returned either way, in the same
// per-sample order, so which matrix cells already exist is decided from
// identical rows. Reading them all before the first CreateJob is safe
// because jobs are keyed by sample and each sample is visited once.
func (b *Builder) jobsForSamples(ctx context.Context, eligible []*sampleData) (map[string][]serverstore.JobRow, error) {
	out := make(map[string][]serverstore.JobRow, len(eligible))
	phases := builderPhases(ctx)
	page, ok := b.Store.(jobPageStore)
	if !ok {
		for _, sd := range eligible {
			readPhase := phases.begin(phaseMatrixJobHistoryRead)
			rows, err := b.Store.JobsForSample(ctx, sd.row.SampleID)
			items, bytes := jobRowsMetrics(rows)
			counters := builderPhaseCounters{logicalCalls: 1, callsKnown: true, pages: 1, items: items, bytes: bytes}
			readPhase.end(err, counters)
			phases.add(phaseMatrixJobs, counters.withoutItems())
			if err != nil {
				return nil, fmt.Errorf("compatibility: jobs for %s: %w", sd.row.SampleID, err)
			}
			out[sd.row.SampleID] = rows
		}
		return out, nil
	}
	for start := 0; start < len(eligible); start += jobProbeBatch {
		end := start + jobProbeBatch
		if end > len(eligible) {
			end = len(eligible)
		}
		ids := make([]string, 0, end-start)
		for _, sd := range eligible[start:end] {
			ids = append(ids, sd.row.SampleID)
		}
		readPhase := phases.begin(phaseMatrixJobHistoryRead)
		rows, err := page.JobsForSamples(ctx, ids)
		items, bytes := jobPagesMetrics(rows)
		counters := builderPhaseCounters{logicalCalls: 1, callsKnown: true, pages: 1, items: items, bytes: bytes}
		readPhase.end(err, counters)
		phases.add(phaseMatrixJobs, counters.withoutItems())
		if err != nil {
			return nil, fmt.Errorf("compatibility: jobs for sample page: %w", err)
		}
		for sampleID, jobs := range rows {
			out[sampleID] = append(out[sampleID], jobs...)
		}
	}
	return out, nil
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

// evidenceForPackage gathers every version's evidence for ONE package.
//
// Per package, and not the whole set at once, because the caller consumes it
// one package at a time and the whole set does not fit. Measured on
// production 2026-09-01: the server was OOM-killed eight times, anon-rss
// 694MB against a 768MiB limit, while a full pass held the corpus twice over
// -- once in byPkg and once in this function's output, plus a dedup index the
// same size again. evidence_agg had gone from roughly 72k rows to 216k that
// day. The kill left lastRun zero, which forces the next pass to be full
// too, so the process rebuilt the same map and died again on a five-minute
// cycle.
//
// One symbol reaches the server under two spellings -- the scanner's
// qualified name on anonymous evidence, the author's bare one on a signed
// receipt -- and both become live snapshot targets. EvidenceForTarget answers
// either with the same rows on purpose (symbolSpellings), which is right per
// target and wrong here, where every target's rows are summed into one
// per-package bucket: the shared rows would be counted once per spelling.
// Production carried a failure cluster of 520 for 260 observed failures
// because of it. Identity, not arrival, decides what is counted -- and the
// dedup index is now one package's worth rather than the corpus's.
type parsedTarget struct {
	target  serverstore.SnapshotTarget
	version string
}

func (b *Builder) evidenceForPackage(ctx context.Context, k pkgKey,
	pkgTargets []parsedTarget, byPkg map[pkgKey]symVer,
) (map[string][]serverstore.EvidenceRow, error) {
	out := map[string][]serverstore.EvidenceRow{}
	seen := map[evidenceKey]bool{}
	add := func(version string, rows []serverstore.EvidenceRow) {
		for _, row := range rows {
			if seen[keyOf(row)] {
				continue
			}
			seen[keyOf(row)] = true
			out[version] = append(out[version], row)
		}
	}
	for _, versions := range byPkg[k] {
		for version, rows := range versions {
			add(version, rows)
		}
	}
	for _, pt := range pkgTargets {
		if _, loaded := byPkg[k][pt.target.Symbol][pt.version]; loaded {
			continue // this pass already read it
		}
		rows, err := b.Store.EvidenceForTarget(ctx, pt.target.PURL, pt.target.Symbol)
		builderPhases(ctx).add(phaseClusterRead, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(rows))})
		if err != nil {
			return nil, fmt.Errorf("compatibility: cluster evidence for %s %q: %w", pt.target.PURL, pt.target.Symbol, err)
		}
		add(pt.version, rows)
	}
	return out, nil
}
