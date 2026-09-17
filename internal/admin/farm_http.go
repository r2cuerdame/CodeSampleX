package admin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Instance is one machine the operator pays for.
//
// The cost is configured rather than fetched. Reading it from AWS would mean
// putting a credential on the server that an operator token already opens, and
// the number it would return is a fixed bundle price the operator already
// knows. A figure typed once is worth less than an API call and costs a great
// deal less to be wrong about.
type Instance struct {
	Name       string
	MonthlyUSD float64
}

// farmCoreSnapshot is the set of required farm measurements. The store calls
// are independent, but a single operator poll must not be allowed to occupy
// the whole background share of the database pool. Two workers overlap the
// slow aggregate scans while leaving half of the default four-connection
// background lane available to the builder and other background work.
type farmCoreSnapshot struct {
	workers      []serverstore.FarmWorker
	health       serverstore.FarmHealth
	backlog      serverstore.FarmBacklog
	completeness serverstore.FarmCompleteness
	at           [4]time.Time
	available    [4]bool
	errs         [4]error
}

const (
	farmWorkersSection = iota
	farmHealthSection
	farmBacklogSection
	farmCompletenessSection
)

// farmSectionMemo is one fixed-size, last-good cache. A successful empty
// result is present; the bool is what keeps "measured as none" distinct from
// "the database did not answer".
type farmSectionMemo[T any] struct {
	mu      sync.Mutex
	value   T
	at      time.Time
	present bool
	retryAt time.Time
}

func (m *farmSectionMemo[T]) read(
	ctx context.Context,
	now time.Time,
	ttl, backoff time.Duration,
	completedAt func() time.Time,
	load func(context.Context) (T, error),
) (T, time.Time, bool, error) {
	m.mu.Lock()
	if m.present && now.Before(m.at.Add(ttl)) {
		value, at, present := m.value, m.at, m.present
		m.mu.Unlock()
		return value, at, present, nil
	}
	if now.Before(m.retryAt) {
		value, at, present := m.value, m.at, m.present
		m.mu.Unlock()
		return value, at, present, errFarmSectionRefreshDeferred
	}
	m.mu.Unlock()

	value, err := load(ctx)
	finished := completedAt().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		// A browser going away is not evidence that PostgreSQL needs a shared
		// cooldown. The route's own deadline is: without a cooldown every
		// minute would repeat the same losing corpus scan.
		if !errors.Is(ctx.Err(), context.Canceled) {
			m.retryAt = finished.Add(backoff)
		}
		return m.value, m.at, m.present, err
	}
	m.value, m.at, m.present = value, finished, true
	m.retryAt = time.Time{}
	return value, finished, true, nil
}

var errFarmSectionRefreshDeferred = errors.New("farm section refresh deferred")

func (m *farmSectionMemo[T]) peek() (T, time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.value, m.at, m.present
}

type farmCoreMemo struct {
	workers      farmSectionMemo[[]serverstore.FarmWorker]
	health       farmSectionMemo[serverstore.FarmHealth]
	backlog      farmSectionMemo[serverstore.FarmBacklog]
	completeness farmSectionMemo[serverstore.FarmCompleteness]
}

const (
	// Worker/session state changes promptly; corpus-wide stocks do not. Both
	// caches are fixed-size and retain their last successful value after TTL.
	farmLiveSectionTTL  = time.Minute
	farmStockSectionTTL = 10 * time.Minute
	// A timed-out corpus scan should not be launched again by the next browser
	// poll. Live state gets a shorter retry because it is operationally useful.
	farmLiveSectionBackoff  = 2 * time.Minute
	farmStockSectionBackoff = 15 * time.Minute
)

func (h *handler) collectFarmCore(ctx context.Context, since, now time.Time) farmCoreSnapshot {
	var snapshot farmCoreSnapshot
	tasks := []func() error{
		func() error {
			var err error
			snapshot.workers, snapshot.at[farmWorkersSection], snapshot.available[farmWorkersSection], err = h.farmCore.workers.read(
				ctx, now, farmLiveSectionTTL, farmLiveSectionBackoff, h.now,
				func(ctx context.Context) ([]serverstore.FarmWorker, error) {
					return h.farmStats.FarmWorkers(ctx, since, now)
				})
			return err
		},
		func() error {
			var err error
			snapshot.health, snapshot.at[farmHealthSection], snapshot.available[farmHealthSection], err = h.farmCore.health.read(
				ctx, now, farmLiveSectionTTL, farmLiveSectionBackoff, h.now,
				func(ctx context.Context) (serverstore.FarmHealth, error) {
					return h.farmStats.FarmHealthNow(ctx, now)
				})
			return err
		},
		func() error {
			var err error
			snapshot.backlog, snapshot.at[farmBacklogSection], snapshot.available[farmBacklogSection], err = h.farmCore.backlog.read(
				ctx, now, farmStockSectionTTL, farmStockSectionBackoff, h.now,
				func(ctx context.Context) (serverstore.FarmBacklog, error) {
					return h.farmStats.FarmBacklogNow(ctx, since, now)
				})
			return err
		},
		func() error {
			var err error
			snapshot.completeness, snapshot.at[farmCompletenessSection], snapshot.available[farmCompletenessSection], err = h.farmCore.completeness.read(
				ctx, now, farmStockSectionTTL, farmStockSectionBackoff, h.now,
				func(ctx context.Context) (serverstore.FarmCompleteness, error) {
					return h.farmStats.FarmCompletenessNow(ctx)
				})
			return err
		},
	}

	queue := make(chan int, len(tasks))
	for i := range tasks {
		queue <- i
	}
	close(queue)
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() {
			defer workers.Done()
			for i := range queue {
				snapshot.errs[i] = tasks[i]()
			}
		}()
	}
	workers.Wait()
	return snapshot
}

func (h *handler) cachedFarmCore() farmCoreSnapshot {
	var snapshot farmCoreSnapshot
	snapshot.workers, snapshot.at[farmWorkersSection], snapshot.available[farmWorkersSection] = h.farmCore.workers.peek()
	snapshot.health, snapshot.at[farmHealthSection], snapshot.available[farmHealthSection] = h.farmCore.health.peek()
	snapshot.backlog, snapshot.at[farmBacklogSection], snapshot.available[farmBacklogSection] = h.farmCore.backlog.peek()
	snapshot.completeness, snapshot.at[farmCompletenessSection], snapshot.available[farmCompletenessSection] = h.farmCore.completeness.peek()
	return snapshot
}

func (s farmCoreSnapshot) refreshFailed() bool {
	for _, err := range s.errs {
		if err != nil {
			return true
		}
	}
	return false
}

// farmWindow is how far back the panel counts a worker's output. Long enough
// to survive one slow job, short enough that a worker that stopped an hour ago
// stops looking productive.
const farmWindow = time.Hour

// A farm snapshot reads the whole corpus, but it is still a request a browser
// is waiting for. The store retains a 25-second ceiling for offline callers;
// the HTTP route gets a much smaller budget and returns whichever independently
// cached sections answered rather than turning one slow stock into a 503.
const farmRequestTimeout = 5 * time.Second

func (h *handler) farm(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	// No store means no numbers. Rendering zeros would make "nothing measured"
	// and "nothing wrong" look identical, which is the failure this panel was
	// built to stop.
	if h.farmStats == nil {
		http.Error(w, "팜 지표를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	refresh := false
	select {
	case h.farmGate <- struct{}{}:
		refresh = true
	default:
	}
	now := h.now().UTC()
	core := h.cachedFarmCore()
	var coverage []serverstore.FarmAxisCoverage
	var coverageAt, coverageGeneratedAt time.Time
	// lastIngestAt/lastIngestCheckedAt answer CSX-453's "is evidence
	// landing" beside coverage: only this poll's winner asks PostgreSQL, and
	// only when nothing else in this poll has already missed its budget.
	var lastIngestAt, lastIngestCheckedAt time.Time
	if refresh {
		defer func() { <-h.farmGate }()
		ctx, cancel := context.WithTimeout(r.Context(), farmRequestTimeout)
		defer cancel()
		core = h.collectFarmCore(ctx, now.Add(-farmWindow), now)
		// Coverage and ingest observability are optional and read separately
		// from the required sections. Skip them after a section has already
		// missed its budget rather than spend more of a request a browser is
		// waiting on.
		if !core.refreshFailed() {
			coverage, coverageAt, coverageGeneratedAt = h.coverage(ctx, now)
			lastIngestAt, lastIngestCheckedAt = h.lastFarmIngestAt(ctx, now)
		} else {
			coverage, coverageAt, coverageGeneratedAt = h.cachedCoverage()
			lastIngestAt, lastIngestCheckedAt = h.cachedLastFarmIngestAt()
		}
	} else {
		coverage, coverageAt, coverageGeneratedAt = h.cachedCoverage()
		lastIngestAt, lastIngestCheckedAt = h.cachedLastFarmIngestAt()
	}
	workers, health := core.workers, core.health

	// The ClassFarmIngest pool row is counters, not a query, so it is read
	// fresh on every request regardless of the refresh gate above.
	var poolClasses []serverstore.ClassPoolStats
	if h.poolStats != nil {
		poolClasses = h.poolStats.PoolStats().Classes
	}

	var views []map[string]any
	if core.available[farmWorkersSection] {
		views = make([]map[string]any, 0, len(workers))
		for _, worker := range workers {
			view := map[string]any{
				"label":        worker.Label,
				"computerName": worker.ComputerName,
				// A session issued but never refreshed is a worker that failed to
				// start. It reads as healthy in every list that shows only labels.
				"started":   !worker.LastRefreshAt.IsZero(),
				"drafts":    worker.Drafts,
				"published": worker.Published,
				"holding":   worker.Holding,
				"issuedAt":  worker.IssuedAt.UTC().Format(time.RFC3339),
				"expiresAt": worker.IdleExpiresAt.UTC().Format(time.RFC3339),
			}
			if !worker.LastRefreshAt.IsZero() {
				view["lastRefreshAt"] = worker.LastRefreshAt.UTC().Format(time.RFC3339)
				view["perHour"] = float64(worker.Drafts) / farmWindow.Hours()
			}
			views = append(views, view)
		}
	}

	instances := make([]map[string]any, 0, len(h.instances))
	total := 0.0
	for _, instance := range h.instances {
		instances = append(instances, map[string]any{
			"name": instance.Name, "monthlyUsd": instance.MonthlyUSD,
		})
		total += instance.MonthlyUSD
	}

	// The same window as the worker rates above, so every number on the panel
	// is over one period. Two windows on one screen is how a reader ends up
	// comparing an hour against a day without noticing.
	var healthView, backlogView, completenessView map[string]any
	if core.available[farmHealthSection] {
		healthView = farmHealthView(health)
	}
	if core.available[farmBacklogSection] {
		backlogView = farmBacklogView(core.backlog)
	}
	if core.available[farmCompletenessSection] {
		completenessView = farmCompletenessView(core.completeness)
	}
	var coverageView []map[string]any
	if !coverageAt.IsZero() {
		coverageView = farmCoverageView(coverage)
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"workers":        views,
		"workersAt":      adminTimeOrEmpty(core.at[farmWorkersSection]),
		"health":         healthView,
		"healthAt":       adminTimeOrEmpty(core.at[farmHealthSection]),
		"backlog":        backlogView,
		"backlogAt":      adminTimeOrEmpty(core.at[farmBacklogSection]),
		"completeness":   completenessView,
		"completenessAt": adminTimeOrEmpty(core.at[farmCompletenessSection]),
		"coverage":       coverageView,
		// When this process last successfully read coverage. It can be
		// minutes old -- the read is memoized and, on failure, held. A stale
		// number that says its age is a different claim from a stale number
		// that does not.
		"coverageAt": adminTimeOrEmpty(coverageAt),
		// When the Builder pass that computed the current value actually
		// ran (CSX-452). The read itself is now cheap and bounded, so the
		// real freshness question an operator has is how stale the
		// Builder's last pass is, not how recently this process asked.
		// Empty means no pass has published yet.
		"coverageGeneratedAt": adminTimeOrEmpty(coverageGeneratedAt),
		// farmIngest (CSX-453): when evidence last actually landed
		// (evidence_agg.last_seen) beside the live farm_ingest pool class
		// counters (CSX-461). See farmIngestView.
		"farmIngest":      farmIngestView(lastIngestAt, lastIngestCheckedAt, poolClasses),
		"instances":       instances,
		"monthlyTotalUsd": total,
	})
}

// farmBacklogView reports what is left and how fast it is moving.
//
// The two stocks are reported apart because they are different absences: a
// coverage hole is a release the network watches people use and has never
// proven, and a dependency is a release nobody has reported at all, which only
// a resolved lockfile even names. Pooling them would hide which one the fleet
// is failing to drain.
//
// The flows carry their window explicitly. A rate without its period is the
// kind of number that gets read as a total.
func farmBacklogView(backlog serverstore.FarmBacklog) map[string]any {
	claimed := make(map[string]int, len(backlog.ClaimedByKind))
	claimedAxes := make(map[string]int, len(backlog.ClaimedByAxis))
	handedOut := 0
	for kind, n := range backlog.ClaimedByKind {
		claimed[clampAdminLabel(kind)] = n
		handedOut += n
	}
	for axis, n := range backlog.ClaimedByAxis {
		claimedAxes[clampAdminLabel(axis)] = n
	}
	return map[string]any{
		"coverageHoles":       backlog.CoverageHoles,
		"dependencies":        backlog.Dependencies,
		"requestBacklog":      backlog.RequestBacklog,
		"repeatedMisses":      backlog.RepeatedMisses,
		"resolvedRequests":    backlog.ResolvedRequests,
		"totalRequests":       backlog.TotalRequests,
		"matrixCells":         farmMatrixCellsView(backlog.Matrix),
		"windowSeconds":       int(farmWindow / time.Second),
		"handedOutInWindow":   handedOut,
		"handedOutByKind":     claimed,
		"handedOutByAxis":     claimedAxes,
		"firstProvenInWindow": backlog.FirstProven,
	}
}

// farmMatrixCellsView reports the unbounded PUBLIC symbol x version corpus.
// It intentionally does not apply the package UI's browse-window caps: this
// value is the canonical completeness denominator, not the current viewport.
// The three evidence states remain separate because they require different
// work, and failed or mixed contracts are in none of the passing-only buckets.
func farmMatrixCellsView(cells serverstore.MatrixCells) map[string]any {
	return map[string]any{
		"cells":                     cells.Cells,
		"observed":                  cells.Observed,
		"verifiedNoObservation":     cells.VerifiedNoObservation,
		"unmeasured":                cells.Unmeasured,
		"packagesShowingBothDashes": cells.PackagesShowingBothDashes,
	}
}

// farmCompletenessView reports the corpus by three-axis completeness.
//
// All eight cells, always, including the ones at zero: a cell that appears
// only once it has a value is a cell nobody notices arriving, and two of these
// are zero for a structural reason rather than because the work is done.
//
// The dependency axis is split three ways beside the matrix because "this
// release pulls nothing" and "nobody has resolved this release" are different
// answers and only the first is a fact. A consumer that folded them would
// print "no dependencies" for silence.
func farmCompletenessView(c serverstore.FarmCompleteness) map[string]any {
	states := make(map[string]int, len(c.States))
	for state, n := range c.States {
		states[clampAdminLabel(state)] = n
	}
	return map[string]any{
		"states":               states,
		"dependencyGraph":      c.DependencyGraph,
		"dependencyProvenNone": c.DependencyProvenNone,
		"dependencyUnknown":    c.DependencyUnknown,
	}
}

func farmHealthView(health serverstore.FarmHealth) map[string]any {
	view := map[string]any{
		"publicSamples":        health.PublicSamples,
		"duplicateCoordinates": health.DuplicateCoords,
		"staleClaims":          health.StaleClaims,
		"receiptsByOs":         health.ReceiptsByOS,
		"quarantinedByReason":  quarantineReasonView(health.QuarantinedByReason),
		// How much of the authoring board the queue is refusing to hand out.
		// A withdrawn SAMPLE and a withheld COORDINATE are different acts —
		// one takes back an answer, the other stops asking the question — so
		// they are counted apart rather than pooled into one alarming number.
		"withheldCoordinates": health.WithheldCoordinates,
		"withheldByReason":    quarantineReasonView(health.WithheldByReason),
		// Verification work no verifier image in this build can run. It is
		// reported apart from the queue depth because it is not a backlog
		// anyone is behind on: waiting does not consume it.
		"unsupportedJobs": health.UnsupportedJobs,
	}
	// The rate is what an operator reads; the count is what they act on.
	if health.PublicSamples > 0 {
		view["duplicateRate"] = float64(health.DuplicateCoords) / float64(health.PublicSamples)
	}
	return view
}

// farmCoverageView reports coverage per (platform, ecosystem).
//
// buildable is the field that matters. npm on Windows is thousands of
// packages observed and zero proven, and it will stay zero forever because no
// Windows Node image exists -- rendered as a progress bar it would read as a
// backlog somebody is behind on. An unbuildable cell omits proven entirely
// rather than reporting a zero, which is this file's existing idiom for
// "not measurable" versus "measured as none".
func farmCoverageView(cells []serverstore.FarmAxisCoverage) []map[string]any {
	out := make([]map[string]any, 0, len(cells))
	for i, c := range cells {
		if i >= maxFarmCoverageRows {
			break
		}
		row := map[string]any{
			"os":        clampAdminLabel(c.OS),
			"ecosystem": clampAdminLabel(c.Ecosystem),
			"observed":  c.Observed,
			"buildable": farmBuildable(c.OS, c.Ecosystem),
		}
		if row["buildable"].(bool) {
			row["measured"] = c.Measured
			row["proven"] = c.Proven
			row["observedProven"] = c.ObservedProven
		}
		out = append(out, row)
	}
	return out
}

// farmBuildable answers whether a verifier could ever run this ecosystem on
// this platform. macOS cannot be containerised at all, and on Windows only
// golang and pypi publish a base image.
func farmBuildable(os, ecosystem string) bool {
	switch strings.ToLower(strings.TrimSpace(os)) {
	case "linux":
		return true
	case "windows":
		return sandbox.SupportsWindows(ecosystem)
	}
	return false
}

// clampAdminLabel bounds a value that came from recorded evidence rather than
// from a fixed vocabulary. validEnv imposes no length limit on either field.
func clampAdminLabel(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxAdminLabelBytes {
		return v[:maxAdminLabelBytes]
	}
	return v
}

const (
	maxFarmCoverageRows = 64
	maxAdminLabelBytes  = 48
)

// quarantineReasonView orders withdrawals by how many share a reason, and
// clamps the text: the reason is operator-written prose, not a vocabulary.
//
// The unexplained bucket is labelled rather than dropped. Something was
// pulled and nobody wrote down why, which is the row an operator most needs
// to see; leaving it blank would make it look like a rendering gap.
func quarantineReasonView(byReason map[string]int) []map[string]any {
	type row struct {
		reason string
		n      int
	}
	rows := make([]row, 0, len(byReason))
	for reason, n := range byReason {
		rows = append(rows, row{reason, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].reason < rows[j].reason
	})
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		if i >= maxQuarantineReasons {
			break
		}
		reason := strings.TrimSpace(r.reason)
		if len(reason) > maxQuarantineReasonBytes {
			reason = reason[:maxQuarantineReasonBytes] + "…"
		}
		out = append(out, map[string]any{
			"reason":      reason,
			"count":       r.n,
			"unexplained": reason == "",
		})
	}
	return out
}

const (
	maxQuarantineReasons     = 12
	maxQuarantineReasonBytes = 96
)

// farmCoverageMemo holds the last coverage answer and when it is worth asking
// for another one.
//
// Before CSX-452, FarmCoverage read the whole corpus -- every evidence_agg
// row joined to packages, plus every receipt's resolved package list
// expanded -- under a 25s ceiling, and the panel it feeds refreshes on a 60s
// browser timer. Measured on production 2026-09-04 (v0.1.129): every poll
// for the whole half-hour in the log hit the ceiling and logged "continuing
// with empty coverage", so the server spent 25 of every 60 seconds computing
// a number it then discarded. This avoidable work shared PostgreSQL CPU and
// connections with public reads.
//
// GetFarmCoverage now reads the Builder-materialized farm_coverage table
// instead: a small whole-table read, not a corpus join. This memo's TTL and
// failure backoff stay in place regardless -- the read is cheap, but a
// transient DB error is still worth a bounded pause rather than retrying on
// every poll, and the last-known-good contract to the caller is unchanged.
//
// generatedAt is a second, independent timestamp: when the Builder pass that
// computed the current value actually ran, as opposed to at, when this
// process last read it. A cheap read model can be re-read every poll and
// still answer a value that is hours old if the Builder has not run --
// generatedAt is what tells an operator that, at is what tells this process
// whether to bother asking again.
type farmCoverageMemo struct {
	mu          sync.Mutex
	value       []serverstore.FarmAxisCoverage
	at          time.Time
	generatedAt time.Time
	retryAt     time.Time
}

const (
	// farmCoverageTTL is how long a computed coverage keeps answering.
	farmCoverageTTL = 10 * time.Minute
	// farmCoverageBackoff is how long a coverage that hit its ceiling stops
	// being retried. Deliberately longer than the TTL: the panel is not owed
	// a fresh number more than it is owed a responsive site.
	farmCoverageBackoff = 15 * time.Minute
)

// coverage answers with a memoized farm coverage, reading the Builder's
// published farm_coverage table only when the last read is stale and the
// last failure is far enough behind. It returns the coverage rows, when this
// process last successfully read them (at), and when the Builder pass that
// computed them actually ran (generatedAt) -- zero when no pass has
// published yet.
func (h *handler) coverage(ctx context.Context, now time.Time) ([]serverstore.FarmAxisCoverage, time.Time, time.Time) {
	h.farmCoverage.mu.Lock()
	if (!h.farmCoverage.at.IsZero() && now.Sub(h.farmCoverage.at) < farmCoverageTTL) ||
		now.Before(h.farmCoverage.retryAt) {
		value, at, generatedAt := h.farmCoverage.value, h.farmCoverage.at, h.farmCoverage.generatedAt
		h.farmCoverage.mu.Unlock()
		return value, at, generatedAt
	}
	h.farmCoverage.mu.Unlock()

	value, generatedAt, found, err := h.farmStats.GetFarmCoverage(ctx)
	completedAt := h.now().UTC()
	if !found {
		// Not yet computed by any Builder pass (e.g. a fresh install) --
		// distinct from a pass that published an empty table.
		value = nil
		generatedAt = time.Time{}
	}

	h.farmCoverage.mu.Lock()
	defer h.farmCoverage.mu.Unlock()
	if err != nil {
		// A disconnected or expired caller is not a shared database failure.
		// Its partial request budget must not defer other operators' refreshes.
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return h.farmCoverage.value, h.farmCoverage.at, h.farmCoverage.generatedAt
		}
		// Not "empty coverage": the panel showing nothing and the panel
		// showing what was last measured are different claims, and only the
		// second one is true here.
		log.Printf("admin: farm coverage read failed (%v); serving the last computed coverage and not retrying for %s",
			err, farmCoverageBackoff)
		h.farmCoverage.retryAt = completedAt.Add(farmCoverageBackoff)
		return h.farmCoverage.value, h.farmCoverage.at, h.farmCoverage.generatedAt
	}
	h.farmCoverage.value = value
	h.farmCoverage.at = completedAt
	h.farmCoverage.generatedAt = generatedAt
	h.farmCoverage.retryAt = time.Time{}
	return value, completedAt, generatedAt
}

func (h *handler) cachedCoverage() ([]serverstore.FarmAxisCoverage, time.Time, time.Time) {
	h.farmCoverage.mu.Lock()
	defer h.farmCoverage.mu.Unlock()
	return h.farmCoverage.value, h.farmCoverage.at, h.farmCoverage.generatedAt
}

// ---------------------------------------------------------- farm ingest --
//
// CSX-453: "is evidence landing" server-side, beside the existing
// ClassFarmIngest pool counters (CSX-461). Farm's own queue-depth counters
// (PR #134, Farm repo) live in Farm's local health-report.json -- that is
// the complementary *local* signal and is deliberately not duplicated here.
// This panel only ever answers what has actually landed in evidence_agg.

// farmIngestMemo holds the last "when did evidence land" answer and when it
// is worth asking PostgreSQL for another one.
//
// LastFarmIngestAt (CSX-453) is a single MAX(last_seen) aggregate over an
// already-indexed leading column -- cheap, unlike the corpus-wide reads
// farmCoverageMemo replaced -- but it is still one round trip a browser poll
// should not repeat every few seconds, and mirrors farmCoverageMemo's exact
// shape: value is the signal itself, at is when this process last read it
// successfully, retryAt is the failure cooldown.
type farmIngestMemo struct {
	mu      sync.Mutex
	value   time.Time // last time evidence actually landed; zero = none yet
	at      time.Time // when this process last successfully read it
	retryAt time.Time
}

const (
	// farmIngestTTL is how long a read LastFarmIngestAt keeps answering.
	// Deliberately much shorter than farmCoverageTTL: this is a single
	// indexed aggregate, not a corpus join, so there is no reason to make an
	// operator wait ten minutes to see a new commit land.
	farmIngestTTL = time.Minute
	// farmIngestBackoff is how long a failed read stops being retried --
	// shorter than farmCoverageBackoff for the same reason the TTL is
	// shorter: the query itself is cheap, so the failure is more likely
	// transient than structural.
	farmIngestBackoff = 2 * time.Minute
)

// lastFarmIngestAt answers a memoized LastFarmIngestAt, reading PostgreSQL
// only when the last read is stale and the last failure is far enough
// behind. It returns the last time evidence actually landed (zero = never)
// and when this process last successfully checked.
func (h *handler) lastFarmIngestAt(ctx context.Context, now time.Time) (time.Time, time.Time) {
	h.farmIngest.mu.Lock()
	if (!h.farmIngest.at.IsZero() && now.Sub(h.farmIngest.at) < farmIngestTTL) ||
		now.Before(h.farmIngest.retryAt) {
		value, at := h.farmIngest.value, h.farmIngest.at
		h.farmIngest.mu.Unlock()
		return value, at
	}
	h.farmIngest.mu.Unlock()

	value, found, err := h.farmStats.LastFarmIngestAt(ctx)
	completedAt := h.now().UTC()
	if !found {
		// No evidence at all (fresh install) is a real, distinct answer from
		// a read failure -- render it as "never", not as an error.
		value = time.Time{}
	}

	h.farmIngest.mu.Lock()
	defer h.farmIngest.mu.Unlock()
	if err != nil {
		// A disconnected or expired caller is not a shared database failure.
		if ctx.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
			return h.farmIngest.value, h.farmIngest.at
		}
		log.Printf("admin: farm last-ingest read failed (%v); serving the last known value and not retrying for %s",
			err, farmIngestBackoff)
		h.farmIngest.retryAt = completedAt.Add(farmIngestBackoff)
		return h.farmIngest.value, h.farmIngest.at
	}
	h.farmIngest.value = value
	h.farmIngest.at = completedAt
	h.farmIngest.retryAt = time.Time{}
	return value, completedAt
}

func (h *handler) cachedLastFarmIngestAt() (time.Time, time.Time) {
	h.farmIngest.mu.Lock()
	defer h.farmIngest.mu.Unlock()
	return h.farmIngest.value, h.farmIngest.at
}

// farmIngestView composes the panel: when evidence last actually landed
// beside the live ClassFarmIngest pool counters (CSX-461). classes is the
// whole pool table; only the farm_ingest row is rendered here, filtered by
// its own String() rather than a copied literal so a rename of the class
// cannot silently stop matching.
func farmIngestView(lastIngestAt, checkedAt time.Time, classes []serverstore.ClassPoolStats) map[string]any {
	view := map[string]any{
		"lastIngestAt": adminTimeOrEmpty(lastIngestAt),
		"checkedAt":    adminTimeOrEmpty(checkedAt),
	}
	wantClass := serverstore.ClassFarmIngest.String()
	for _, c := range classes {
		if c.Class != wantClass {
			continue
		}
		view["pool"] = map[string]any{
			"limit":    c.Limit,
			"inUse":    c.InUse,
			"attempts": c.Attempts,
			"acquired": c.Acquired,
			"waited":   c.Waited,
			"waitMax":  formatPoolWait(c.WaitMax),
			"busy":     c.Busy,
			"timeouts": c.Timeouts,
			"failed":   c.Failed,
			"canceled": c.Canceled,
		}
		break
	}
	return view
}

// adminTimeOrEmpty renders a timestamp the panel may not have. A zero time is
// "never computed", which is not the same as a time and must not render as
// one.
func adminTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
