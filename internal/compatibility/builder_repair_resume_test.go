package compatibility

// #517: an exhaustive pass that cannot finish inside its ceiling must be
// continued by the next pass, not started over.
//
// Production published stats generatedAt=2026-09-17T12:34:46Z for nine days.
// Past resumeWindow every pass must be exhaustive, and an exhaustive pass that
// is stopped at the ceiling kept nothing: the next attempt began from the
// first package again. These tests stop a repair part-way, start a new
// process on the same store, and require it to finish only what was left --
// and to finish with exactly the outputs a single uninterrupted pass builds.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// blockingShardStore stalls every shard write for one package until the
// pass's context ends, which is what a pass that cannot finish inside its
// ceiling looks like from the store's side.
type blockingShardStore struct {
	*serverstore.Fake
	mu        sync.Mutex
	blockPkg  string // "ecosystem/name/" prefix of the shard key to stall
	shardKeys []string
}

func (s *blockingShardStore) PutShard(ctx context.Context, key, etag, shardJSON string) error {
	s.mu.Lock()
	block := s.blockPkg != "" && strings.HasPrefix(key, s.blockPkg)
	s.shardKeys = append(s.shardKeys, key)
	s.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Fake.PutShard(ctx, key, etag, shardJSON)
}

func (s *blockingShardStore) takeShardKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.shardKeys
	s.shardKeys = nil
	return out
}

// seedRepairCorpus puts several independent packages next to the axios
// fixture, so a repair has more than one chunk to walk.
func seedRepairCorpus(t *testing.T, store serverstore.Store) {
	t.Helper()
	seedBuilderFixture(t, store)
	var batches []domain.ObservationBatch
	for _, name := range []string{"alpha", "bravo", "charlie", "delta", "echo"} {
		batches = append(batches, domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-08-13", AnonID: "peer-" + name, ProjectBucket: "proj-" + name,
			Package: "pkg:npm/" + name + "@1.0.0", Symbol: name + ".run", SymbolConfidence: domain.SymbolProbable,
			Environment: envNode("esm"), Stage: domain.StageProjectCompile, Result: domain.ResultPass,
			ObservationCount: 3,
		})
	}
	if acc, rej, err := store.IngestBatches(context.Background(), batches); err != nil || acc != len(batches) || len(rej) != 0 {
		t.Fatalf("ingest: acc=%d rej=%v err=%v", acc, rej, err)
	}
}

// writeStaleStats leaves the rollup a completed pass wrote long ago: the
// production state this Issue was opened on.
func writeStaleStats(t *testing.T, store serverstore.Store, generatedAt time.Time) {
	t.Helper()
	doc := fmt.Sprintf(`{"generatedAt":%q}`, generatedAt.UTC().Format(time.RFC3339))
	if err := store.SetStatsDaily(context.Background(), generatedAt.Format("2006-01-02"), doc); err != nil {
		t.Fatal(err)
	}
}

func readBuilderStatus(t *testing.T, store serverstore.BuilderStatusStore) BuilderStatus {
	t.Helper()
	js, ok, err := store.GetBuilderStatus(context.Background(), BuilderStatusName)
	if err != nil || !ok {
		t.Fatalf("builder status: ok=%t err=%v", ok, err)
	}
	var st BuilderStatus
	if err := json.Unmarshal([]byte(js), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

type materializedOutputs struct {
	shards    map[string]string
	snapshots map[serverstore.SnapshotTarget]string
}

func readOutputs(t *testing.T, store serverstore.Store) materializedOutputs {
	t.Helper()
	ctx := context.Background()
	out := materializedOutputs{shards: map[string]string{}, snapshots: map[serverstore.SnapshotTarget]string{}}
	keys, err := store.ShardKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		_, js, _, err := store.GetShard(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		out.shards[key] = js
	}
	targets, err := store.SnapshotKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		js, _, err := store.GetSnapshot(ctx, target.PURL, target.Symbol)
		if err != nil {
			t.Fatal(err)
		}
		out.snapshots[target] = js
	}
	return out
}

func TestAStalledRepairIsContinuedNotRestarted(t *testing.T) {
	now := time.Date(2026, 9, 26, 1, 50, 0, 0, time.UTC)
	stale := time.Date(2026, 9, 17, 12, 34, 46, 0, time.UTC)
	clock := func() time.Time { return now }

	fake := serverstore.NewFake()
	fake.NowFn = clock
	seedRepairCorpus(t, fake)
	writeStaleStats(t, fake, stale)
	store := &blockingShardStore{Fake: fake, blockPkg: "npm/charlie/"}

	// The first process: the stamp is 8.5 days old, so this pass must be
	// exhaustive. It is walked two packages at a time and stalls on charlie
	// until its ceiling fires.
	first := &Builder{Store: store, Now: clock, repairChunk: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	err := first.RunOnce(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled pass returned %v, want a deadline", err)
	}
	firstKeys := store.takeShardKeys()

	st := readBuilderStatus(t, fake)
	if st.LastPassOutcome != PassReasonTimeout || st.LastFailureReason != PassReasonTimeout {
		t.Fatalf("status after the stalled pass = outcome %q reason %q, want timeout", st.LastPassOutcome, st.LastFailureReason)
	}
	if st.LastSuccessAt != "" {
		t.Fatalf("a pass that never finished was recorded as a success at %s", st.LastSuccessAt)
	}
	if st.Repair == nil {
		t.Fatal("the stalled repair left no progress on record; the next process would start over")
	}
	// alpha and axios sort first and form the first chunk.
	if st.Repair.Cursor != "npm/axios" || st.Repair.PackagesDone != 2 {
		t.Fatalf("repair progress = cursor %q done %d, want cursor npm/axios done 2", st.Repair.Cursor, st.Repair.PackagesDone)
	}
	if st.Repair.ChunkPackages != 1 {
		t.Errorf("a chunk that hit the ceiling did not shrink the next chunk: chunk=%d", st.Repair.ChunkPackages)
	}
	if got := generatedAtOf(t, fake); got != stale.Format(time.RFC3339) {
		t.Fatalf("stats generatedAt moved to %s on a pass that did not finish", got)
	}

	// A new process -- a restart, a redeploy, or the lease moving -- on the
	// same store, with nothing carried in memory, and the stall cleared.
	store.mu.Lock()
	store.blockPkg = ""
	store.mu.Unlock()
	now = now.Add(10 * time.Minute)
	second := &Builder{Store: store, Now: clock}
	if err := second.RunOnce(context.Background()); err != nil {
		t.Fatalf("the continuing pass failed: %v", err)
	}
	secondKeys := store.takeShardKeys()
	for _, key := range secondKeys {
		if strings.HasPrefix(key, "npm/alpha/") || strings.HasPrefix(key, "npm/axios/") {
			t.Errorf("the continuing pass rebuilt %s, which the stalled pass had already committed", key)
		}
	}
	for _, want := range []string{"npm/bravo/", "npm/charlie/", "npm/delta/", "npm/echo/"} {
		found := false
		for _, key := range secondKeys {
			found = found || strings.HasPrefix(key, want)
		}
		if !found {
			t.Errorf("the continuing pass never built %s*", want)
		}
	}
	if len(firstKeys) == 0 {
		t.Fatal("the stalled pass committed nothing")
	}

	repairStart := now.Add(-10 * time.Minute).Format(time.RFC3339)
	if got := generatedAtOf(t, fake); got != repairStart {
		t.Fatalf("stats generatedAt after the repair = %s, want the repair's start %s", got, repairStart)
	}
	st = readBuilderStatus(t, fake)
	if st.LastPassOutcome != PassOutcomeSuccess || st.LastSuccessAt != now.Format(time.RFC3339) ||
		st.LastSuccessGeneratedAt != repairStart || st.Repair != nil || st.ConsecutiveFailures != 0 {
		t.Fatalf("status after the repair completed = %+v", st)
	}
	if st.LastFailureReason != PassReasonTimeout {
		t.Errorf("the last failure reason was forgotten on success: %q", st.LastFailureReason)
	}

	// The next tick is ordinary incremental work, not another repair: the
	// repair's start stamp is only minutes old.
	now = now.Add(5 * time.Minute)
	if err := second.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := readBuilderStatus(t, fake); st.LastPassFull || st.Repair != nil {
		t.Fatalf("the pass after a completed repair was exhaustive again: full=%t repair=%+v", st.LastPassFull, st.Repair)
	}

	// Chunking changes when work happens, never what it builds: the outputs
	// equal a single uninterrupted full pass over the same corpus.
	oracle := serverstore.NewFake()
	seededAt := time.Date(2026, 9, 26, 1, 50, 0, 0, time.UTC) // when fake was seeded
	oracle.NowFn = func() time.Time { return seededAt }
	seedRepairCorpus(t, oracle)
	oracleNow := now
	whole := &Builder{Store: oracle, Now: func() time.Time { return oracleNow }}
	if err := whole.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Rebuild the chunked store's outputs at the same clock too, so the
	// comparison is about content rather than timestamps.
	now = oracleNow
	again := &Builder{Store: fake, Now: clock}
	again.repair = nil
	if err := again.runRepair(context.Background(), now, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, want := readOutputs(t, fake), readOutputs(t, oracle)
	if len(got.shards) != len(want.shards) || len(got.snapshots) != len(want.snapshots) {
		t.Fatalf("chunked repair built %d shards/%d snapshots, a full pass %d/%d",
			len(got.shards), len(got.snapshots), len(want.shards), len(want.snapshots))
	}
	for key, js := range want.shards {
		if got.shards[key] != js {
			t.Errorf("shard %s differs between a chunked repair and a full pass:\nchunked %s\nfull    %s", key, got.shards[key], js)
		}
	}
	for key, js := range want.snapshots {
		if got.snapshots[key] != js {
			t.Errorf("snapshot %s %q differs between a chunked repair and a full pass", key.PURL, key.Symbol)
		}
	}
}

// The same stall inside one long-lived process: the next pass the loop runs
// continues the walk from memory.
func TestTheSameProcessContinuesItsOwnStalledRepair(t *testing.T) {
	now := time.Date(2026, 9, 26, 1, 50, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	fake := serverstore.NewFake()
	fake.NowFn = clock
	seedRepairCorpus(t, fake)
	writeStaleStats(t, fake, now.Add(-205*time.Hour))
	store := &blockingShardStore{Fake: fake, blockPkg: "npm/delta/"}

	b := &Builder{Store: store, Now: clock, repairChunk: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	if err := b.RunOnce(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled pass returned %v", err)
	}
	cancel()
	store.takeShardKeys()
	store.mu.Lock()
	store.blockPkg = ""
	store.mu.Unlock()

	now = now.Add(5 * time.Minute)
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, key := range store.takeShardKeys() {
		if strings.HasPrefix(key, "npm/alpha/") || strings.HasPrefix(key, "npm/axios/") || strings.HasPrefix(key, "npm/bravo/") {
			t.Errorf("the continuing pass rebuilt %s from the committed first chunk", key)
		}
	}
	if st := readBuilderStatus(t, fake); st.LastPassOutcome != PassOutcomeSuccess || st.Repair != nil {
		t.Fatalf("repair did not complete in the same process: %+v", st)
	}
}

// A pass stopped because the lease was lost says so, rather than reading as
// an ordinary shutdown.
func TestALeaseLossIsRecordedAsTheFailureReason(t *testing.T) {
	fake := serverstore.NewFake()
	fake.NowFn = func() time.Time { return testNow }
	seedBuilderFixture(t, fake)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fmt.Errorf("%w: renew refused", ErrBuilderLeaseLost))
	b := &Builder{Store: fake, Now: func() time.Time { return testNow }}
	if err := b.RunOnce(ctx); err == nil {
		t.Fatal("a pass whose lease was lost reported success")
	}
	st := readBuilderStatus(t, fake)
	if st.LastFailureReason != PassReasonLeaseLost {
		t.Fatalf("failure reason = %q, want %q", st.LastFailureReason, PassReasonLeaseLost)
	}
	if st.ConsecutiveFailures != 1 {
		t.Errorf("consecutive failures = %d, want 1", st.ConsecutiveFailures)
	}
}

// A single exhaustive pass that fails makes the next one chunked, so the
// hourly repair cannot become the endless rebuild either.
func TestAFailedFullPassMakesTheNextOneResumable(t *testing.T) {
	fake := serverstore.NewFake()
	fake.NowFn = func() time.Time { return testNow }
	seedRepairCorpus(t, fake)
	store := &blockingShardStore{Fake: fake, blockPkg: "npm/charlie/"}
	b := &Builder{Store: store, Now: func() time.Time { return testNow }, repairChunk: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	if err := b.RunOnce(ctx); err == nil {
		t.Fatal("stalled cold-start pass reported success")
	}
	cancel()
	if b.repair != nil {
		t.Fatal("a cold start with no history was chunked; it should keep the single pass")
	}
	if !b.fullAttemptFailed {
		t.Fatal("the failed exhaustive pass was not remembered")
	}
	store.mu.Lock()
	store.blockPkg = ""
	store.mu.Unlock()
	if err := b.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := readBuilderStatus(t, fake); st.LastPassOutcome != PassOutcomeSuccess || !st.LastPassFull {
		t.Fatalf("the retry was not a completed exhaustive pass: %+v", st)
	}
	if b.fullAttemptFailed || b.repair != nil {
		t.Fatalf("completed repair left state behind: failed=%t repair=%+v", b.fullAttemptFailed, b.repair)
	}
}
