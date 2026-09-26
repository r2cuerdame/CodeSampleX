package compatibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The Builder's own record of its passes (#517).
//
// From 2026-09-17T12:34:46Z production published the same stats generatedAt
// for nine days. Everything the site could say about why was that one stamp
// not moving: whether passes started, hit the six-hour ceiling, lost the
// leader lease or failed on an error was visible only in container logs
// nobody without a host key could read. And the stamp itself could not move,
// because a pass that has been failing for more than resumeWindow must be an
// exhaustive one, and an exhaustive pass that cannot finish inside the
// ceiling starts from nothing on the next attempt, forever.
//
// This file holds both halves of the answer. BuilderStatus is written at the
// end of every pass, whatever ended it, and is served publicly at
// GET /v1/builder. The repair walk makes an exhaustive pass resumable: it is
// run as a sequence of package chunks, each committed and recorded before the
// next starts, so a pass stopped part-way is continued from the last chunk it
// finished -- by the next pass in the same process, or by the next process.

// BuilderStatusName keys the status row. It is the lease name: one pipeline,
// one record.
const BuilderStatusName = DefaultLeaseName

// Pass outcomes and failure reasons as published. They are a closed set so a
// monitor can alert on the value without parsing prose; the error text itself
// stays in the log, where a database message cannot leak to the public.
const (
	PassOutcomeSuccess   = "success"
	PassReasonTimeout    = "timeout"
	PassReasonLeaseLost  = "lease_lost"
	PassReasonCanceled   = "canceled"
	PassReasonError      = "error"
	statusWriteTimeout   = 5 * time.Second
	defaultRepairChunk   = 200
	repairChunkFloor     = 1
	statusTimestampShape = time.RFC3339
)

// ErrBuilderLeaseLost is the cause a Leader cancels its runWhileLeader
// context with when a renew is refused, so the pass that was running can be
// recorded as lost to the lease rather than as an ordinary shutdown.
var ErrBuilderLeaseLost = errors.New("compatibility: builder lease lost")

// BuilderStatus is the published record. Timestamps are RFC 3339 UTC; empty
// means "never".
type BuilderStatus struct {
	LastPassStartedAt  string `json:"lastPassStartedAt,omitempty"`
	LastPassFinishedAt string `json:"lastPassFinishedAt,omitempty"`
	LastPassOutcome    string `json:"lastPassOutcome,omitempty"`
	LastPassFull       bool   `json:"lastPassFull"`

	// LastSuccessAt is when the last successful pass finished;
	// LastSuccessGeneratedAt is the stamp that pass wrote into the stats
	// rollup (its start, which is what the next pass resumes from).
	LastSuccessAt          string `json:"lastSuccessAt,omitempty"`
	LastSuccessGeneratedAt string `json:"lastSuccessGeneratedAt,omitempty"`

	LastFailureAt string `json:"lastFailureAt,omitempty"`
	// LastFailureReason is one of timeout, lease_lost, canceled, error.
	LastFailureReason string `json:"lastFailureReason,omitempty"`
	// LastFailurePhase is the builder phase the failure surfaced in, and
	// LastFailureClass the coarse error class the phase log already uses.
	LastFailurePhase    string `json:"lastFailurePhase,omitempty"`
	LastFailureClass    string `json:"lastFailureClass,omitempty"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`

	// Repair is the exhaustive pass currently being walked in chunks, nil
	// when none is.
	Repair *RepairProgress `json:"repair,omitempty"`
}

// RepairProgress is how far an exhaustive repair has got. Cursor is the last
// package ("ecosystem/name", lowercased) whose chunk committed; packages sort
// by that string, so everything at or before it is done.
type RepairProgress struct {
	StartedAt     string `json:"startedAt"`
	Generation    uint64 `json:"generation"`
	Cursor        string `json:"cursor"`
	PackagesDone  int    `json:"packagesDone"`
	PackagesTotal int    `json:"packagesTotal"`
	Chunks        int    `json:"chunks"`
	// ChunkPackages is the chunk size the next attempt uses. It halves when
	// a chunk itself cannot finish inside the pass ceiling, so no single
	// chunk can wedge the walk the way the whole corpus did.
	ChunkPackages int `json:"chunkPackages"`
}

func formatStatusTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(statusTimestampShape)
}

func parseStatusTime(s string) time.Time {
	t, err := time.Parse(statusTimestampShape, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// loadStatus reads the durable record once per process. A store without the
// capability, or one that cannot answer, leaves an empty record: status is
// observability, and must never be the reason a pass does not run.
func (b *Builder) loadStatus(ctx context.Context) {
	if b.statusLoaded {
		return
	}
	b.statusLoaded = true
	store, ok := b.Store.(serverstore.BuilderStatusStore)
	if !ok {
		return
	}
	js, found, err := store.GetBuilderStatus(ctx, BuilderStatusName)
	if err != nil || !found {
		return
	}
	var st BuilderStatus
	if json.Unmarshal([]byte(js), &st) != nil {
		return
	}
	b.status = st
	if st.Repair != nil && parseStatusTime(st.Repair.StartedAt).IsZero() {
		b.status.Repair = nil
	}
	// A walk the previous process left part-way is this process's to finish.
	if b.repair == nil {
		b.repair = b.status.Repair
	}
}

// saveStatus writes the record on a context detached from the pass: the pass
// that most needs recording is the one whose context just ended.
func (b *Builder) saveStatus(ctx context.Context) {
	store, ok := b.Store.(serverstore.BuilderStatusStore)
	if !ok {
		return
	}
	js, err := json.Marshal(b.status)
	if err != nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusWriteTimeout)
	defer cancel()
	if err := store.PutBuilderStatus(writeCtx, BuilderStatusName, string(js)); err != nil {
		log.Printf("compatibility: pass-status write failed: %v", err)
	}
}

// passFailureReason classifies how a pass that did not succeed ended.
func passFailureReason(ctx context.Context, err error) string {
	switch {
	case errors.Is(context.Cause(ctx), ErrBuilderLeaseLost):
		return PassReasonLeaseLost
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return PassReasonTimeout
	case errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		return PassReasonCanceled
	default:
		return PassReasonError
	}
}

// recordPassEnd updates and persists the record for one finished pass and
// writes the one log line that names how it ended.
func (b *Builder) recordPassEnd(ctx context.Context, startedAt time.Time, full bool, runErr error, failedPhase string) {
	finished := b.now()
	st := &b.status
	st.LastPassStartedAt = formatStatusTime(startedAt)
	st.LastPassFinishedAt = formatStatusTime(finished)
	st.LastPassFull = full
	if runErr == nil {
		st.LastPassOutcome = PassOutcomeSuccess
		st.LastSuccessAt = formatStatusTime(finished)
		st.LastSuccessGeneratedAt = formatStatusTime(b.lastRun)
		st.ConsecutiveFailures = 0
		log.Printf("compatibility: pass-end outcome=success full=%t started=%s elapsed=%s",
			full, st.LastPassStartedAt, finished.Sub(startedAt))
	} else {
		reason := passFailureReason(ctx, runErr)
		if failedPhase == "" {
			failedPhase = "none"
		}
		st.LastPassOutcome = reason
		st.LastFailureAt = formatStatusTime(finished)
		st.LastFailureReason = reason
		st.LastFailurePhase = failedPhase
		st.LastFailureClass = builderPhaseErrorClass(runErr)
		st.ConsecutiveFailures++
		lastSuccess := "never"
		if st.LastSuccessAt != "" {
			lastSuccess = st.LastSuccessAt
		}
		log.Printf("compatibility: pass-end outcome=failure reason=%s phase=%s full=%t started=%s elapsed=%s consecutive_failures=%d last_success=%s err=%v",
			reason, failedPhase, full, st.LastPassStartedAt, finished.Sub(startedAt), st.ConsecutiveFailures, lastSuccess, runErr)
	}
	st.Repair = b.repair
	b.saveStatus(ctx)
}

// repairPackage is one package of the repair universe: every shard key of
// every major, so a chunk always rebuilds a package whole.
type repairPackage struct {
	id   string
	keys []shardKey
}

func repairPackageID(k shardKey) string {
	return k.ecosystem + "/" + strings.ToLower(k.name)
}

// shardKeyOf parses a stored shard key, "ecosystem/name/major". The name may
// itself contain slashes (Go module paths), so ecosystem is before the first
// and major after the last.
func shardKeyOf(key string) (shardKey, bool) {
	first := strings.Index(key, "/")
	last := strings.LastIndex(key, "/")
	if first <= 0 || last <= first+1 || last == len(key)-1 {
		return shardKey{}, false
	}
	return shardKey{ecosystem: key[:first], name: key[first+1 : last], major: key[last+1:]}, true
}

// repairInputs is what every chunk of one repair walk reads: the complete
// target list, every sample with its receipts, and the package universe
// derived from them. It is loaded once per walk in a process -- the same
// whole-corpus reads a single exhaustive pass makes -- so walking in chunks
// does not multiply them.
type repairInputs struct {
	targets  []serverstore.SnapshotTarget
	samples  []sampleData
	universe []repairPackage
}

// loadRepairInputs reads the corpus a repair walks. The universe is every
// package an exhaustive pass would touch: each live target, each package a
// sample or receipt defines a shard for, and each package that still has a
// stored snapshot or shard. The stored half is what lets a chunked repair
// retire outputs whose inputs have gone, as a single exhaustive pass does.
func (b *Builder) loadRepairInputs(ctx context.Context, phases *builderPhaseRecorder) (*repairInputs, error) {
	phase := phases.begin(phaseListTargets)
	targets, err := b.Store.ListSnapshotTargets(ctx)
	phase.end(err, builderPhaseCounters{logicalCalls: 1, callsKnown: true, items: int64(len(targets))})
	phases.close(phaseListTargets)
	if err != nil {
		return nil, fmt.Errorf("compatibility: repair targets: %w", err)
	}
	phase = phases.begin(phaseLoadSamples)
	samples, err := b.loadSamplesForPackages(ctx, nil)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseLoadSamples)
	if err != nil {
		return nil, err
	}
	if err := b.publishReceiptPackages(ctx, phases, samples); err != nil {
		return nil, err
	}
	stored, err := b.Store.SnapshotKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("compatibility: repair snapshot keys: %w", err)
	}
	shards, err := b.Store.ShardKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("compatibility: repair shard keys: %w", err)
	}
	var samplePURLs []string
	for _, sd := range samples {
		for _, p := range sampleShardPURLs(sd) {
			samplePURLs = append(samplePURLs, p.String())
		}
	}
	keys := affectedKeys(serverstore.Changes{
		Targets:     append(append([]serverstore.SnapshotTarget{}, targets...), stored...),
		SamplePURLs: samplePURLs,
	})
	for _, raw := range shards {
		if k, ok := shardKeyOf(raw); ok {
			keys[k] = true
		}
	}
	byID := map[string]*repairPackage{}
	for k := range keys {
		id := repairPackageID(k)
		if byID[id] == nil {
			byID[id] = &repairPackage{id: id}
		}
		byID[id].keys = append(byID[id].keys, k)
	}
	universe := make([]repairPackage, 0, len(byID))
	for _, p := range byID {
		universe = append(universe, *p)
	}
	sort.Slice(universe, func(i, j int) bool { return universe[i].id < universe[j].id })
	return &repairInputs{targets: targets, samples: samples, universe: universe}, nil
}

// needsChunkedRepair decides whether a pass that must be exhaustive is walked
// in chunks. A repair already under way always continues. Otherwise chunking
// is for the exhaustive passes that have a reason to fear the ceiling: the
// last completed pass is older than resumeWindow, or the previous exhaustive
// attempt in this process did not finish. A cold start with no history and
// the ordinary hourly repair keep the single pass they have always taken.
func (b *Builder) needsChunkedRepair(now time.Time) bool {
	if b.repair != nil || b.staleStamp || b.fullAttemptFailed {
		return true
	}
	return !b.lastRun.IsZero() && now.Sub(b.lastProgressAt()) > resumeWindow
}

// lastProgressAt is when this builder last finished a pass: the resume stamp
// or, after a pass completed here, its completion time. The staleness rule
// measures absence of progress. Measured from lastRun alone, a repair that
// took longer than resumeWindow to walk would finish, stamp its start, and
// immediately be judged too old to resume from -- starting another repair.
func (b *Builder) lastProgressAt() time.Time {
	if b.lastCompletedAt.After(b.lastRun) {
		return b.lastCompletedAt
	}
	return b.lastRun
}

// runRepair walks one exhaustive repair forward as far as this pass's
// context allows, committing and recording each chunk, and completes it when
// the last chunk lands.
func (b *Builder) runRepair(ctx context.Context, now time.Time, generation uint64, started time.Time) error {
	if b.repair != nil && b.repair.Generation != generation {
		// A projection backfill committed since the walk began. Chunks built
		// before it were built from the old projection; start over.
		log.Printf("compatibility: repair restarted: repair generation moved from %d to %d", b.repair.Generation, generation)
		b.repair, b.repairCache = nil, nil
	}
	if b.repair == nil {
		chunk := b.repairChunk
		if chunk <= 0 {
			chunk = defaultRepairChunk
		}
		b.repair = &RepairProgress{
			StartedAt: formatStatusTime(now), Generation: generation, ChunkPackages: chunk,
		}
		b.repairCache = nil
	}
	r := b.repair
	if r.ChunkPackages < repairChunkFloor {
		r.ChunkPackages = defaultRepairChunk
	}
	repairStart := parseStatusTime(r.StartedAt)

	b.unscoped = true
	defer func() { b.unscoped = false }()
	phases := builderPhases(ctx)
	if b.repairCache == nil {
		inputs, err := b.loadRepairInputs(ctx, phases)
		if err != nil {
			return err
		}
		b.repairCache = inputs
	}
	inputs := b.repairCache
	universe := inputs.universe
	r.PackagesTotal = len(universe)
	next := sort.Search(len(universe), func(i int) bool { return universe[i].id > r.Cursor })
	log.Printf("compatibility: repair resume started=%s cursor=%q done=%d total=%d chunk=%d",
		r.StartedAt, r.Cursor, r.PackagesDone, r.PackagesTotal, r.ChunkPackages)

	for next < len(universe) {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(next+r.ChunkPackages, len(universe))
		chunk := universe[next:end]
		affected := map[shardKey]bool{}
		for _, p := range chunk {
			for _, k := range p.keys {
				affected[k] = true
			}
		}
		chunkPhases := b.newPhaseRecorder(ctx)
		chunkCtx := withBuilderPhaseRecorder(ctx, chunkPhases)
		chunkStarted := time.Now()
		_, err := b.materialize(chunkCtx, chunkPhases, affected, repairStart, false, inputs)
		if err == nil && chunkCtx.Err() != nil {
			err = chunkCtx.Err()
		}
		chunkPhases.finish(err)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && r.ChunkPackages > repairChunkFloor {
				r.ChunkPackages = max(r.ChunkPackages/2, repairChunkFloor)
			}
			return fmt.Errorf("compatibility: repair chunk %s..%s: %w", chunk[0].id, chunk[len(chunk)-1].id, err)
		}
		r.Cursor = chunk[len(chunk)-1].id
		r.PackagesDone += len(chunk)
		r.Chunks++
		next = end
		log.Printf("compatibility: repair chunk committed through=%q packages=%d done=%d total=%d elapsed=%s",
			r.Cursor, len(chunk), r.PackagesDone, r.PackagesTotal, time.Since(chunkStarted))
		b.status.Repair = r
		b.saveStatus(ctx)
	}

	// Every package is rebuilt. What is left is corpus-wide and cheap next to
	// the walk: matrix jobs, coverage, the dependency axis, and the clock.
	if err := b.publishMatrixJobs(ctx, phases, inputs.samples); err != nil {
		return err
	}
	if err := b.computeAndPublishFarmCoverage(ctx); err != nil {
		return fmt.Errorf("compatibility: put farm coverage: %w", err)
	}
	if err := b.publishDependencyAxis(ctx, phases); err != nil {
		return err
	}
	phase := phases.begin(phaseRefreshStats)
	err := b.refreshStats(ctx, repairStart)
	phase.end(err, builderPhaseCounters{callsKnown: true})
	phases.close(phaseRefreshStats)
	if err != nil {
		return err
	}
	b.passes++
	b.lastRun = repairStart
	b.lastCompletedAt = b.now()
	b.fullRepairAt = b.now().Add(time.Hour)
	b.completedRepairGeneration = generation
	chunks, packages := r.Chunks, r.PackagesDone
	b.repair, b.repairCache = nil, nil
	b.staleStamp, b.fullAttemptFailed = false, false
	log.Printf("compatibility: builder pass complete full=true repair=true since=%s chunks=%d packages=%d total=%s",
		r.StartedAt, chunks, packages, time.Since(started))
	return nil
}
