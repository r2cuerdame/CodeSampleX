package compatibility

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type phaseLogSink struct {
	lines []string
}

func (s *phaseLogSink) logf(format string, args ...any) {
	s.lines = append(s.lines, fmt.Sprintf(format, args...))
}

func (s *phaseLogSink) joined() string { return strings.Join(s.lines, "\n") }

func (s *phaseLogSink) matching(parts ...string) []string {
	var out []string
	for _, line := range s.lines {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, line)
		}
	}
	return out
}

type advancingPhaseClock struct {
	now  time.Time
	step time.Duration
}

func (c *advancingPhaseClock) Now() time.Time {
	now := c.now
	c.now = c.now.Add(c.step)
	return now
}

type manualPhaseClock struct{ now time.Time }

func (c *manualPhaseClock) Now() time.Time                 { return c.now }
func (c *manualPhaseClock) Advance(duration time.Duration) { c.now = c.now.Add(duration) }

func testPhaseBuilder(store serverstore.Store, sink *phaseLogSink) *Builder {
	clock := &advancingPhaseClock{now: testNow, step: 10 * time.Millisecond}
	return &Builder{
		Store:     store,
		Now:       func() time.Time { return testNow },
		phaseNow:  clock.Now,
		phaseLogf: sink.logf,
	}
}

type phaseSequenceStore struct {
	serverstore.Store
	calls          []string
	listTargetsErr error
	setStatsErr    error
}

func (s *phaseSequenceStore) record(call string) { s.calls = append(s.calls, call) }

func (s *phaseSequenceStore) GetLatestStats(ctx context.Context) (string, bool, error) {
	s.record("GetLatestStats")
	return s.Store.GetLatestStats(ctx)
}

func (s *phaseSequenceStore) ChangedSince(ctx context.Context, since time.Time) (serverstore.Changes, error) {
	s.record("ChangedSince")
	return s.Store.ChangedSince(ctx, since)
}

func (s *phaseSequenceStore) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.record("ListSnapshotTargets")
	if s.listTargetsErr != nil {
		return nil, s.listTargetsErr
	}
	return s.Store.ListSnapshotTargets(ctx)
}

func (s *phaseSequenceStore) ListSamplesPage(ctx context.Context, limit, offset int) ([]serverstore.SampleRow, error) {
	s.record("ListSamplesPage")
	return s.Store.ListSamplesPage(ctx, limit, offset)
}

func (s *phaseSequenceStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	s.record("SnapshotKeys")
	return s.Store.SnapshotKeys(ctx)
}

func (s *phaseSequenceStore) DeleteSnapshots(ctx context.Context, targets []serverstore.SnapshotTarget) error {
	s.record("DeleteSnapshots")
	return s.Store.DeleteSnapshots(ctx, targets)
}

func (s *phaseSequenceStore) ShardKeys(ctx context.Context) ([]string, error) {
	s.record("ShardKeys")
	return s.Store.ShardKeys(ctx)
}

func (s *phaseSequenceStore) NetworkCounts(ctx context.Context, now time.Time) (serverstore.NetworkCounts, error) {
	s.record("NetworkCounts")
	return s.Store.NetworkCounts(ctx, now)
}

func (s *phaseSequenceStore) AdoptionSummary(ctx context.Context) (serverstore.AdoptionCounts, error) {
	s.record("AdoptionSummary")
	return s.Store.AdoptionSummary(ctx)
}

func (s *phaseSequenceStore) SetStatsDaily(ctx context.Context, day, statsJSON string) error {
	s.record("SetStatsDaily")
	if s.setStatsErr != nil {
		return s.setStatsErr
	}
	return s.Store.SetStatsDaily(ctx, day, statsJSON)
}

func TestBuilderPhaseEmptyFullPassPreservesSequenceAndBookkeeping(t *testing.T) {
	fake := serverstore.NewFake()
	store := &phaseSequenceStore{Store: fake}
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(store, sink)

	if err := builder.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wantCalls := []string{
		"GetLatestStats", "ListSnapshotTargets", "ListSamplesPage", "SnapshotKeys",
		"DeleteSnapshots", "ShardKeys", "NetworkCounts", "AdoptionSummary", "SetStatsDaily",
	}
	if got := strings.Join(store.calls, ","); got != strings.Join(wantCalls, ",") {
		t.Fatalf("store call sequence = %s, want %s", got, strings.Join(wantCalls, ","))
	}
	if builder.passes != 1 || !builder.lastRun.Equal(testNow) {
		t.Fatalf("bookkeeping passes/lastRun = %d/%s", builder.passes, builder.lastRun)
	}
	if _, ok, err := fake.GetLatestStats(context.Background()); err != nil || !ok {
		t.Fatalf("stats output missing: ok=%t err=%v", ok, err)
	}
	assertPhaseFinal(t, sink, "outcome=success", "error_class=none", "failed_phase=none")
	for _, name := range []string{
		phaseResume, phaseListTargets, phaseLoadSamples, phaseSamplePageRead,
		phaseReceiptPageRead, phaseDecode, phaseSnapshotCalculate, phaseSnapshotWrite,
		phaseClusterRead, phaseClusterCalculate, phaseClusterWrite, phaseShards,
		phaseMatrixJobs, phaseMatrixJobHistoryRead, phaseDependencyAxis, phaseRefreshStats,
	} {
		assertOnePhaseEntryAndExit(t, sink, name)
	}
}

func TestBuilderPhaseEmptyIncrementalPassKeepsFastPath(t *testing.T) {
	fake := serverstore.NewFake()
	store := &phaseSequenceStore{Store: fake}
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(store, sink)
	builder.lastRun = testNow.Add(-5 * time.Minute)
	builder.passes = 1

	if err := builder.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	wantCalls := []string{"ChangedSince", "NetworkCounts", "AdoptionSummary", "SetStatsDaily"}
	if got := strings.Join(store.calls, ","); got != strings.Join(wantCalls, ",") {
		t.Fatalf("store call sequence = %s, want %s", got, strings.Join(wantCalls, ","))
	}
	if builder.passes != 2 || !builder.lastRun.Equal(testNow) {
		t.Fatalf("bookkeeping passes/lastRun = %d/%s", builder.passes, builder.lastRun)
	}
	assertOnePhaseEntryAndExit(t, sink, phaseChanges)
	if got := sink.matching("phase=" + phaseListTargets); len(got) != 0 {
		t.Fatalf("no-change fast path unexpectedly entered list_targets: %v", got)
	}
}

func TestBuilderPhaseFullAndIncrementalPassesExposeFixedCoverage(t *testing.T) {
	fake := serverstore.NewFake()
	seedBuilderFixture(t, fake)
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(fake, sink)

	if err := builder.RunOnce(context.Background()); err != nil {
		t.Fatalf("full pass: %v", err)
	}
	dirtyOnePackage(fake, "pkg:npm/axios@1.12.0", "axios.post")
	if err := builder.RunOnce(context.Background()); err != nil {
		t.Fatalf("incremental pass: %v", err)
	}
	if len(sink.matching("event=final", "outcome=success")) != 2 {
		t.Fatalf("final logs =\n%s", sink.joined())
	}
	for _, name := range []string{
		phaseChanges, phaseListTargets, phaseLoadSamples, phaseSamplePageRead,
		phaseReceiptPageRead, phaseDecode, phaseEnsureReceiptPackages,
		phaseReceiptDerivedCalculation, phaseTargetEvidence, phaseSnapshotCalculate,
		phaseSnapshotWrite, phaseSnapshotRetire, phaseClusterRead,
		phaseClusterCalculate, phaseClusterWrite, phaseShards, phaseMatrixJobs,
		phaseMatrixJobHistoryRead, phaseDependencyAxis, phaseRefreshStats,
	} {
		if len(sink.matching("phase="+name)) == 0 {
			t.Errorf("phase %s was not recorded", name)
		}
	}
}

func TestBuilderPhaseEarlyAndLateFailuresAreFinalizedWithoutLeak(t *testing.T) {
	const private = "PRIVATE-SQL-and-id-should-never-appear"
	for _, tc := range []struct {
		name      string
		configure func(*phaseSequenceStore)
		failed    string
		wantCalls []string
	}{
		{
			name: "early list targets", failed: phaseListTargets,
			configure: func(store *phaseSequenceStore) { store.listTargetsErr = errors.New(private) },
			wantCalls: []string{"GetLatestStats", "ListSnapshotTargets"},
		},
		{
			name: "late stats write", failed: phaseRefreshStats,
			configure: func(store *phaseSequenceStore) { store.setStatsErr = errors.New(private) },
			wantCalls: []string{
				"GetLatestStats", "ListSnapshotTargets", "ListSamplesPage", "SnapshotKeys",
				"DeleteSnapshots", "ShardKeys", "NetworkCounts", "AdoptionSummary", "SetStatsDaily",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &phaseSequenceStore{Store: serverstore.NewFake()}
			tc.configure(store)
			sink := &phaseLogSink{}
			builder := testPhaseBuilder(store, sink)
			if err := builder.RunOnce(context.Background()); err == nil {
				t.Fatal("RunOnce succeeded")
			}
			if got := strings.Join(store.calls, ","); got != strings.Join(tc.wantCalls, ",") {
				t.Fatalf("store call sequence = %s, want %s", got, strings.Join(tc.wantCalls, ","))
			}
			assertPhaseFinal(t, sink, "outcome=error", "error_class=error", "active_phase="+tc.failed, "failed_phase="+tc.failed)
			assertPositivePartialDuration(t, sink, tc.failed)
			if strings.Contains(sink.joined(), private) {
				t.Fatalf("phase log leaked private fixture: %s", sink.joined())
			}
			if builder.passes != 0 || !builder.lastRun.IsZero() {
				t.Fatalf("failed pass changed bookkeeping: %d/%s", builder.passes, builder.lastRun)
			}
		})
	}
}

type dependencyAxisFaultStore struct {
	*phaseSequenceStore
	err error
}

func (s *dependencyAxisFaultStore) DependencyAxisOpen(context.Context, int, int) ([]serverstore.DependencyAxisWork, error) {
	s.record("DependencyAxisOpen")
	return nil, s.err
}

func TestBuilderPhaseBareDependencyAxisErrorIsAttributedWithoutCauseClaim(t *testing.T) {
	const private = "statement timeout: PRIVATE dependency coordinate"
	store := &dependencyAxisFaultStore{
		phaseSequenceStore: &phaseSequenceStore{Store: serverstore.NewFake()},
		err:                errors.New(private),
	}
	sink := &phaseLogSink{}
	if err := testPhaseBuilder(store, sink).RunOnce(context.Background()); err == nil || err.Error() != private {
		t.Fatalf("RunOnce error = %v", err)
	}
	assertPhaseFinal(t, sink, "error_class=error", "active_phase="+phaseDependencyAxis, "failed_phase="+phaseDependencyAxis)
	assertPositivePartialDuration(t, sink, phaseDependencyAxis)
	if strings.Contains(sink.joined(), private) || strings.Contains(sink.joined(), "statement timeout") {
		t.Fatalf("phase log leaked or classified the bare error: %s", sink.joined())
	}
	t.Logf("sanitized phase final: %s", sink.matching("event=final")[0])
	t.Log("privacy_fixture_leaks=0")
}

type clusterWriteFaultStore struct {
	serverstore.Store
	err error
}

func (s *clusterWriteFaultStore) UpsertFailureClusters(context.Context, []serverstore.ClusterRow) error {
	return s.err
}

func TestBuilderPhaseFailedClusterWriteRetainsPartialElapsed(t *testing.T) {
	fake := serverstore.NewFake()
	seedBuilderFixture(t, fake)
	store := &clusterWriteFaultStore{Store: fake, err: errors.New("PRIVATE cluster payload")}
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(store, sink)
	if err := builder.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce succeeded")
	}
	assertPhaseFinal(t, sink, "error_class=error", "active_phase="+phaseClusterWrite, "failed_phase="+phaseClusterWrite)
	assertPositivePartialDuration(t, sink, phaseClusterWrite)
	if strings.Contains(sink.joined(), "PRIVATE") {
		t.Fatalf("phase log leaked cluster fixture: %s", sink.joined())
	}
	if builder.passes != 0 || !builder.lastRun.IsZero() {
		t.Fatalf("failed cluster write changed bookkeeping: %d/%s", builder.passes, builder.lastRun)
	}
}

type cancelListTargetsStore struct{ serverstore.Store }

func (s *cancelListTargetsStore) ListSnapshotTargets(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	return nil, ctx.Err()
}

func TestBuilderPhaseCancellationIsFinalized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(&cancelListTargetsStore{Store: serverstore.NewFake()}, sink)
	if err := builder.RunOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunOnce error = %v, want context canceled", err)
	}
	assertPhaseFinal(t, sink, "outcome=canceled", "error_class=canceled", "active_phase="+phaseListTargets, "failed_phase="+phaseListTargets)
	assertPositivePartialDuration(t, sink, phaseListTargets)
}

func TestBuilderPhaseRetrySeriesUsesDistinctProcessAttemptsWithoutBookkeeping(t *testing.T) {
	const scripted = "scripted retry failure"
	store := &phaseSequenceStore{Store: serverstore.NewFake(), listTargetsErr: errors.New(scripted)}
	sink := &phaseLogSink{}
	builder := testPhaseBuilder(store, sink)
	var waits int
	runBuilderLoopWith(context.Background(), time.Minute, builder.RunOnce,
		func(context.Context, time.Duration) bool {
			waits++
			return waits < 6
		}, func(time.Duration) time.Duration { return 0 })

	finals := sink.matching("event=final")
	if len(finals) != 6 {
		t.Fatalf("final count = %d, want 6", len(finals))
	}
	attemptPattern := regexp.MustCompile(`attempt=([0-9]+)`)
	seen := map[string]bool{}
	for _, line := range finals {
		match := attemptPattern.FindStringSubmatch(line)
		if len(match) != 2 || seen[match[1]] {
			t.Fatalf("attempt identity missing or reused: %q", line)
		}
		seen[match[1]] = true
	}
	if builder.passes != 0 || !builder.lastRun.IsZero() {
		t.Fatalf("retry/defer changed pass bookkeeping: %d/%s", builder.passes, builder.lastRun)
	}
	if strings.Contains(sink.joined(), scripted) {
		t.Fatalf("phase log leaked retry fixture: %s", sink.joined())
	}
}

func TestBuilderPhaseRepeatedWorkIsAggregatedAndProgressIsRateLimited(t *testing.T) {
	clock := &manualPhaseClock{now: testNow}
	sink := &phaseLogSink{}
	budgetCtx := serverstore.WithQueryBudget(context.Background(), serverstore.NewQueryBudget(serverstore.ClassBackground))
	recorder := newBuilderPhaseRecorder(budgetCtx, clock.Now, sink.logf)

	for i := 0; i < 4; i++ {
		token := recorder.begin(phaseSamplePageRead)
		clock.Advance(time.Second)
		token.end(nil, builderPhaseCounters{logicalCalls: 1, callsKnown: true, pages: 1, items: 10, bytes: 100})
		recorder.progress(phaseSamplePageRead)
		if i == 0 {
			clock.Advance(28 * time.Second)
		} else {
			clock.Advance(29 * time.Second)
		}
	}
	recorder.close(phaseSamplePageRead)
	recorder.finish(nil)

	assertOnePhaseEntryAndExit(t, sink, phaseSamplePageRead)
	progress := sink.matching("event=progress", "phase="+phaseSamplePageRead)
	if len(progress) != 3 {
		t.Fatalf("progress count = %d, want 3 at >=30s boundaries: %v", len(progress), progress)
	}
	exit := sink.matching("event=exit", "phase="+phaseSamplePageRead)
	for _, want := range []string{
		"elapsed_ms_inclusive=4000", "duration_scope=inclusive_nested_not_additive", "logical_calls=4", "pages=4",
		"items=40", "item_unit=sample_rows_returned",
		"json_bytes_examined_or_constructed_cumulative=400", "json_bytes_coverage=selected_in_memory_values_may_overlap_nested_phases",
		"pool_busy_inclusive=0", "query_timeouts_inclusive=0", "pool_wait_ms_inclusive=0",
		"pressure_scope=accumulated_budget_deltas_inclusive_nested_not_additive", "db_acquisitions=unknown", "db_bytes=unknown",
	} {
		if !strings.Contains(exit[0], want) {
			t.Errorf("exit log missing %q: %s", want, exit[0])
		}
	}
}

func TestBuilderPhaseNestedDurationsAreExplicitlyInclusive(t *testing.T) {
	clock := &manualPhaseClock{now: testNow}
	sink := &phaseLogSink{}
	recorder := newBuilderPhaseRecorder(context.Background(), clock.Now, sink.logf)

	parent := recorder.begin(phaseLoadSamples)
	clock.Advance(time.Second)
	child := recorder.begin(phaseSamplePageRead)
	clock.Advance(2 * time.Second)
	child.end(nil, builderPhaseCounters{logicalCalls: 1, callsKnown: true, pages: 1, items: 3, bytes: 90})
	clock.Advance(3 * time.Second)
	parent.end(nil, builderPhaseCounters{callsKnown: true})
	recorder.close(phaseSamplePageRead)
	recorder.close(phaseLoadSamples)
	recorder.finish(nil)

	childExit := sink.matching("event=exit", "phase="+phaseSamplePageRead)[0]
	parentExit := sink.matching("event=exit", "phase="+phaseLoadSamples)[0]
	for line, wants := range map[string][]string{
		childExit:  {"elapsed_ms_inclusive=2000", "duration_scope=inclusive_nested_not_additive"},
		parentExit: {"elapsed_ms_inclusive=6000", "duration_scope=inclusive_nested_not_additive", "items=0", "item_unit=none"},
	} {
		for _, want := range wants {
			if !strings.Contains(line, want) {
				t.Errorf("phase exit missing %q: %s", want, line)
			}
		}
	}
	final := sink.matching("event=final")[0]
	for _, want := range []string{"attempt_elapsed_ms=6000", "phase_elapsed_ms_inclusive=", "duration_scope=inclusive_nested_not_additive"} {
		if !strings.Contains(final, want) {
			t.Errorf("final log missing %q: %s", want, final)
		}
	}
}

func TestBuilderPhaseLoadSamplesKeepsRecordUnitsSeparate(t *testing.T) {
	fake := serverstore.NewFake()
	seedBuilderFixture(t, fake)
	sink := &phaseLogSink{}
	recorder := newBuilderPhaseRecorder(context.Background(), time.Now, sink.logf)
	ctx := withBuilderPhaseRecorder(context.Background(), recorder)
	token := recorder.begin(phaseLoadSamples)
	_, err := (&Builder{Store: fake}).loadSamples(ctx)
	token.end(err, builderPhaseCounters{callsKnown: true})
	for _, name := range []string{phaseSamplePageRead, phaseReceiptPageRead, phaseDecode, phaseLoadSamples} {
		recorder.close(name)
	}
	if err != nil {
		t.Fatalf("loadSamples: %v", err)
	}
	checks := map[string][]string{
		phaseSamplePageRead:  {"items=1 ", "item_unit=sample_rows_returned"},
		phaseReceiptPageRead: {"items=2 ", "item_unit=receipt_rows_returned"},
		phaseDecode:          {"items=3 ", "item_unit=json_records_decoded"},
		phaseLoadSamples:     {"items=0 ", "item_unit=none"},
	}
	for name, wants := range checks {
		exit := sink.matching("event=exit", "phase="+name)[0]
		for _, want := range wants {
			if !strings.Contains(exit, want) {
				t.Errorf("%s exit missing %q: %s", name, want, exit)
			}
		}
	}
}

func TestBuilderPhaseClusterReadCountsReturnedEvidenceRowsOnly(t *testing.T) {
	fake := serverstore.NewFake()
	purl, _ := seedBuilderFixture(t, fake)
	sink := &phaseLogSink{}
	recorder := newBuilderPhaseRecorder(context.Background(), time.Now, sink.logf)
	ctx := withBuilderPhaseRecorder(context.Background(), recorder)
	token := recorder.begin(phaseClusterRead)
	rowsByVersion, err := (&Builder{Store: fake}).evidenceForPackage(ctx,
		pkgKey{ecosystem: "npm", name: "axios"},
		[]parsedTarget{{target: serverstore.SnapshotTarget{PURL: purl, Symbol: "axios.post"}, version: "1.12.0"}},
		map[pkgKey]symVer{},
	)
	token.end(err, builderPhaseCounters{callsKnown: true})
	recorder.close(phaseClusterRead)
	if err != nil {
		t.Fatalf("evidenceForPackage: %v", err)
	}
	rowCount := 0
	for _, rows := range rowsByVersion {
		rowCount += len(rows)
	}
	exit := sink.matching("event=exit", "phase="+phaseClusterRead)[0]
	for _, want := range []string{fmt.Sprintf("items=%d", rowCount), "item_unit=evidence_rows_returned"} {
		if !strings.Contains(exit, want) {
			t.Errorf("cluster_read exit missing %q: %s", want, exit)
		}
	}
	if strings.Contains(exit, fmt.Sprintf("items=%d ", rowCount+len(rowsByVersion))) {
		t.Fatalf("cluster_read combined evidence rows with version buckets: %s", exit)
	}
}

func TestBuilderPhaseReceiptPackageItemsMatchBulkAndFallback(t *testing.T) {
	purls := []domain.PURL{
		{Ecosystem: "npm", Name: "one", Version: "1.0.0"},
		{Ecosystem: "npm", Name: "two", Version: "2.0.0"},
	}
	samples := []sampleData{{receipts: []ReceiptInfo{{ResolvedPackages: purls}}}}

	for _, tc := range []struct {
		name  string
		store func(*serverstore.Fake) serverstore.Store
	}{
		{name: "fallback", store: func(fake *serverstore.Fake) serverstore.Store {
			return &rowAtATimeStore{newReadCounter(fake)}
		}},
		{name: "bulk", store: func(fake *serverstore.Fake) serverstore.Store {
			return &bulkReadStore{newReadCounter(fake)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := serverstore.NewFake()
			for _, purl := range purls {
				if err := fake.UpsertPackage(context.Background(), serverstore.PackageRow{
					PURL: purl.String(), Ecosystem: purl.Ecosystem, Name: purl.Name, Version: purl.Version,
				}); err != nil {
					t.Fatal(err)
				}
			}
			sink := &phaseLogSink{}
			recorder := newBuilderPhaseRecorder(context.Background(), time.Now, sink.logf)
			ctx := withBuilderPhaseRecorder(context.Background(), recorder)
			token := recorder.begin(phaseEnsureReceiptPackages)
			err := (&Builder{Store: tc.store(fake)}).ensureReceiptPackages(ctx, samples)
			token.end(err, builderPhaseCounters{callsKnown: true})
			recorder.close(phaseEnsureReceiptPackages)
			if err != nil {
				t.Fatalf("ensureReceiptPackages: %v", err)
			}
			exit := sink.matching("event=exit", "phase="+phaseEnsureReceiptPackages)[0]
			for _, want := range []string{"items=2", "item_unit=receipt_packages_requested"} {
				if !strings.Contains(exit, want) {
					t.Errorf("receipt package exit missing %q: %s", want, exit)
				}
			}
		})
	}
}

func TestBuilderPhaseMatrixParentCountsSampleInputsNotJobs(t *testing.T) {
	const sampleID = "sha256:phase-matrix"
	fake := serverstore.NewFake()
	if _, err := fake.CreateJob(context.Background(), serverstore.JobRow{
		SampleID: sampleID, Reason: "other", WantEnvJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	env := bulkMavenEnv()
	env.LanguageVersion = "8"
	samples := []sampleData{{
		row: serverstore.SampleRow{SampleID: sampleID, Status: "CROSS_PASS"},
		manifest: domain.SampleManifest{
			Environment: env, VerifierAdapter: "maven-java@1",
		},
		receipts: []ReceiptInfo{{
			Env: env, ContractResult: "PASS", VerifierAdapter: "maven-java@1",
			SandboxCapability: domain.CapContainerRun,
		}},
	}}
	sink := &phaseLogSink{}
	recorder := newBuilderPhaseRecorder(context.Background(), time.Now, sink.logf)
	ctx := withBuilderPhaseRecorder(context.Background(), recorder)
	token := recorder.begin(phaseMatrixJobs)
	err := (&Builder{Store: &rowAtATimeStore{newReadCounter(fake)}}).createMatrixJobs(ctx, samples)
	token.end(err, builderPhaseCounters{callsKnown: true, items: int64(len(samples))})
	recorder.close(phaseMatrixJobHistoryRead)
	recorder.close(phaseMatrixJobs)
	if err != nil {
		t.Fatalf("createMatrixJobs: %v", err)
	}
	parent := sink.matching("event=exit", "phase="+phaseMatrixJobs)[0]
	for _, want := range []string{"items=1", "item_unit=sample_inputs"} {
		if !strings.Contains(parent, want) {
			t.Errorf("matrix parent exit missing %q: %s", want, parent)
		}
	}
	history := sink.matching("event=exit", "phase="+phaseMatrixJobHistoryRead)[0]
	for _, want := range []string{"items=1", "item_unit=job_rows_returned"} {
		if !strings.Contains(history, want) {
			t.Errorf("matrix history exit missing %q: %s", want, history)
		}
	}
}

func assertOnePhaseEntryAndExit(t *testing.T, sink *phaseLogSink, name string) {
	t.Helper()
	if got := len(sink.matching("event=enter", "phase="+name)); got != 1 {
		t.Fatalf("phase %s enter count = %d, want 1\n%s", name, got, sink.joined())
	}
	if got := len(sink.matching("event=exit", "phase="+name)); got != 1 {
		t.Fatalf("phase %s exit count = %d, want 1\n%s", name, got, sink.joined())
	}
}

func assertPhaseFinal(t *testing.T, sink *phaseLogSink, parts ...string) {
	t.Helper()
	lines := sink.matching("event=final")
	if len(lines) != 1 {
		t.Fatalf("final count = %d, want 1\n%s", len(lines), sink.joined())
	}
	for _, part := range parts {
		if !strings.Contains(lines[0], part) {
			t.Fatalf("final log missing %q: %s", part, lines[0])
		}
	}
}

func assertPositivePartialDuration(t *testing.T, sink *phaseLogSink, name string) {
	t.Helper()
	lines := sink.matching("event=exit", "phase="+name, "outcome=error")
	if len(lines) != 1 {
		t.Fatalf("failed phase exit count = %d, want 1\n%s", len(lines), sink.joined())
	}
	match := regexp.MustCompile(`elapsed_ms_inclusive=([0-9]+)`).FindStringSubmatch(lines[0])
	if len(match) != 2 {
		t.Fatalf("elapsed missing: %s", lines[0])
	}
	millis, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || millis <= 0 {
		t.Fatalf("partial elapsed = %q, want >0", match[1])
	}
}
