package compatibility

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// builderReadCounter counts the store reads an aggregation pass makes, so a
// test can state the footprint of a small changed set as a number rather than
// as an impression.
type builderReadCounter struct {
	*serverstore.Fake
	mu    sync.Mutex
	calls map[string]int
	pages map[string][]int
}

func newReadCounter(f *serverstore.Fake) *builderReadCounter {
	return &builderReadCounter{Fake: f, calls: map[string]int{}, pages: map[string][]int{}}
}

func (c *builderReadCounter) note(name string, size int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls[name]++
	c.pages[name] = append(c.pages[name], size)
}

func (c *builderReadCounter) count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[name]
}

func (c *builderReadCounter) sizes(name string) []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.pages[name]...)
}

func (c *builderReadCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = map[string]int{}
	c.pages = map[string][]int{}
}

func (c *builderReadCounter) GetPackage(ctx context.Context, purl string) (serverstore.PackageRow, bool, error) {
	c.note("GetPackage", 1)
	return c.Fake.GetPackage(ctx, purl)
}

func (c *builderReadCounter) JobsForSample(ctx context.Context, sampleID string) ([]serverstore.JobRow, error) {
	c.note("JobsForSample", 1)
	return c.Fake.JobsForSample(ctx, sampleID)
}

func (c *builderReadCounter) EvidenceForTarget(ctx context.Context, purl, symbol string) ([]serverstore.EvidenceRow, error) {
	c.note("EvidenceForTarget", 1)
	return c.Fake.EvidenceForTarget(ctx, purl, symbol)
}

// rowAtATimeStore is the original contract: one read per package, one per
// sample. Alternate stores still take this path.
type rowAtATimeStore struct{ *builderReadCounter }

// bulkReadStore answers the two bounded-page contracts the builder prefers,
// over exactly the same underlying rows.
type bulkReadStore struct{ *builderReadCounter }

func (s *bulkReadStore) ExistingPackagePURLs(ctx context.Context, purls []string) (map[string]bool, error) {
	s.note("ExistingPackagePURLs", len(purls))
	out := map[string]bool{}
	for _, purl := range purls {
		if _, ok, err := s.Fake.GetPackage(ctx, purl); err != nil {
			return nil, err
		} else if ok {
			out[purl] = true
		}
	}
	return out, nil
}

func (s *bulkReadStore) JobsForSamples(ctx context.Context, sampleIDs []string) (map[string][]serverstore.JobRow, error) {
	s.note("JobsForSamples", len(sampleIDs))
	out := map[string][]serverstore.JobRow{}
	for _, sampleID := range sampleIDs {
		rows, err := s.Fake.JobsForSample(ctx, sampleID)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			out[sampleID] = rows
		}
	}
	return out, nil
}

func (s *bulkReadStore) EvidenceForTargets(ctx context.Context, targets []serverstore.SnapshotTarget) (map[serverstore.SnapshotTarget][]serverstore.EvidenceRow, error) {
	s.note("EvidenceForTargets", len(targets))
	out := make(map[serverstore.SnapshotTarget][]serverstore.EvidenceRow, len(targets))
	for _, target := range targets {
		rows, err := s.Fake.EvidenceForTarget(ctx, target.PURL, target.Symbol)
		if err != nil {
			return nil, err
		}
		out[target] = rows
	}
	return out, nil
}

// bulkCorpus is what both halves of a parity comparison are seeded from: npm
// packages carrying observed PASS and FAIL evidence (so clusters and
// regressions exist), and maven/java samples (so matrix generation runs).
type bulkCorpus struct {
	npmNames   []string
	mavenNames []string
	sampleIDs  []string
}

func seedBulkCorpus(t *testing.T, store *serverstore.Fake, npm, maven int) bulkCorpus {
	t.Helper()
	ctx := context.Background()
	var c bulkCorpus
	// Pin the store clock. Evidence rows carry first/last-seen stamps, and two
	// independently seeded corpora would otherwise differ by the milliseconds
	// between them rather than by anything aggregation did.
	store.NowFn = func() time.Time { return testNow }

	for i := 0; i < npm; i++ {
		name := fmt.Sprintf("npmpkg%03d", i)
		c.npmNames = append(c.npmNames, name)
		symbol := name + ".run"
		// Two versions per package so the regression rule has a V-1 to
		// compare against and clusters span more than one version.
		for _, version := range []string{"1.0.0", "1.1.0"} {
			purl := fmt.Sprintf("pkg:npm/%s@%s", name, version)
			batches := []domain.ObservationBatch{{
				SchemaVersion: 1, Epoch: "2026-08-13", AnonID: fmt.Sprintf("peer%d", i),
				ProjectBucket: fmt.Sprintf("proj%d", i), Package: purl, Symbol: symbol,
				SymbolConfidence: domain.SymbolProbable, Environment: envNode("esm"),
				Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 7,
			}}
			if version == "1.1.0" {
				exitCode := 1
				fail := domain.ObservationBatch{
					SchemaVersion: 1, Epoch: "2026-08-13", AnonID: fmt.Sprintf("peerf%d", i),
					ProjectBucket: fmt.Sprintf("projf%d", i), Package: purl, Symbol: symbol,
					SymbolConfidence: domain.SymbolProbable, Environment: envNode("cjs"),
					Stage: domain.StageProjectCompile, Result: domain.ResultFail, ObservationCount: 4,
					ErrorCode:       "ERR_REQUIRE_ESM",
					TerminationKind: domain.TerminationExit, ExitCode: &exitCode,
					ErrorSummary:    "ERR_REQUIRE_ESM normalized failure",
					EvidenceQuality: domain.EvidenceComplete,
				}
				fail.ErrorFingerprint = domain.FailureFingerprint(fail.Stage,
					domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode},
					fail.ErrorCode, fail.ErrorSummary)
				batches = append(batches, fail)
			}
			if acc, rej, err := store.IngestBatches(ctx, batches); err != nil || acc != len(batches) {
				t.Fatalf("ingest %s: acc=%d rej=%v err=%v", purl, acc, rej, err)
			}
		}

		// One sample per package, verified by a signed v2 receipt whose
		// resolver established the exact version. That receipt is what gives
		// receipt-derived package registration anything to register.
		purl := fmt.Sprintf("pkg:npm/%s@1.1.0", name)
		manifest := domain.SampleManifest{
			SchemaVersion: 1,
			Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "use " + name,
				Packages: []string{purl}, Contract: []string{"works"}},
			Packages: []string{purl}, Symbols: []string{symbol},
			Environment: envNode("esm"), License: "MIT-0",
			ContractCommand: []string{"node", "test/contract.mjs"},
			VerifierAdapter: "node-typescript@1",
		}
		sampleID := fmt.Sprintf("sha256:%064x", 100000+i)
		c.sampleIDs = append(c.sampleIDs, sampleID)
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
			Status: "CROSS_PASS", License: "MIT-0", SizeBytes: 1024, CreatedAt: testNow,
		}); err != nil {
			t.Fatal(err)
		}
		saveBulkReceipt(t, store, sampleID, name, envNode("esm"), []string{purl}, "node-typescript@1")
	}

	for i := 0; i < maven; i++ {
		name := fmt.Sprintf("mvnpkg%03d", i)
		c.mavenNames = append(c.mavenNames, name)
		purl := fmt.Sprintf("pkg:maven/com.example/%s@2.0.0", name)
		env := bulkMavenEnv()
		manifest := domain.SampleManifest{
			SchemaVersion: 1,
			Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "use " + name,
				Packages: []string{purl}, Contract: []string{"works"}},
			Packages: []string{purl}, Symbols: []string{name + ".run"},
			Environment: env, License: "MIT-0",
			ContractCommand: []string{"mvn", "-q", "test"},
			VerifierAdapter: "maven-java@1",
		}
		sampleID := fmt.Sprintf("sha256:%064x", 200000+i)
		c.sampleIDs = append(c.sampleIDs, sampleID)
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
			Status: "CROSS_PASS", License: "MIT-0", SizeBytes: 2048, CreatedAt: testNow,
		}); err != nil {
			t.Fatal(err)
		}
		saveBulkReceipt(t, store, sampleID, name, env, []string{purl}, "maven-java@1")
	}
	return c
}

func bulkMavenEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "maven", OS: "linux", Arch: "amd64",
		OSVersionBucket: "2023", Distro: "amzn", Libc: "glibc",
		Virtualization: "container", ContainerRuntime: "docker",
		Runtime: "java", RuntimeVersion: "17", ExecutionContext: "java",
		Compiler: "javac", CompilerVersion: "17",
		PackageManager: "maven", PackageManagerVersion: "3.9.11",
	}
}

func saveBulkReceipt(t *testing.T, store *serverstore.Fake, sampleID, caseName string,
	env domain.EnvironmentFingerprint, resolved []string, adapter string) {
	t.Helper()
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, CaseID: "case:" + caseName,
		EnvironmentHash: env.Normalize().Hash(), Environment: env,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: resolved,
		VerifierAdapter:  adapter, SandboxCapability: domain.CapContainerRun,
		LogsDigest: "sha256:x", CreatedAt: testNow.Format(time.RFC3339),
		PeerID: "ed25519:" + strings.Repeat("ab", 8),
	}
	if err := store.SaveReceipt(context.Background(), serverstore.ReceiptRow{
		ReceiptID: receipt.ReceiptID(), SampleID: sampleID, PeerID: receipt.PeerID,
		EnvHash: receipt.EnvironmentHash, ReceiptJSON: string(domain.MustCanonicalJSON(receipt)),
		ContractResult: "PASS", CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

// materializedState renders everything an aggregation pass publishes. Two
// stores that agree here served their readers the same answers.
func materializedState(t *testing.T, store *serverstore.Fake, c bulkCorpus) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder

	snapshots, err := store.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].PURL != snapshots[j].PURL {
			return snapshots[i].PURL < snapshots[j].PURL
		}
		return snapshots[i].Symbol < snapshots[j].Symbol
	})
	for _, s := range snapshots {
		fmt.Fprintf(&b, "snapshot|%s|%s|%s\n", s.PURL, s.Symbol, s.SnapshotJSON)
	}

	keys, err := store.ShardKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	for _, key := range keys {
		etag, body, ok, err := store.GetShard(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "shard|%s|%t|%s|%s\n", key, ok, etag, body)
	}

	for _, name := range append(append([]string(nil), c.npmNames...), c.mavenNames...) {
		clusters, err := store.ListFailureClusters(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		rendered := make([]string, 0, len(clusters))
		for _, cl := range clusters {
			// The surrogate id is assigned by the store, not by aggregation.
			cl.ID = 0
			rendered = append(rendered, string(domain.MustCanonicalJSON(cl)))
		}
		sort.Strings(rendered)
		for _, line := range rendered {
			fmt.Fprintf(&b, "cluster|%s|%s\n", name, line)
		}
	}

	for _, name := range c.npmNames {
		rows, err := store.ListPackageVersions(ctx, "npm", name)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			fmt.Fprintf(&b, "package|%s|%s|%s\n", row.PURL, row.Major, row.Publicness)
		}
	}
	for _, name := range c.mavenNames {
		rows, err := store.ListPackageVersions(ctx, "maven", "com.example/"+name)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			fmt.Fprintf(&b, "package|%s|%s|%s\n", row.PURL, row.Major, row.Publicness)
		}
	}

	for _, sampleID := range c.sampleIDs {
		jobs, err := store.JobsForSample(ctx, sampleID)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range jobs {
			fmt.Fprintf(&b, "job|%s|%s|%s|%s\n", sampleID, j.Reason, j.Status, j.WantEnvJSON)
		}
	}
	return b.String()
}

// dirtyOnePackage narrows a pass to a single package, which is the ordinary
// shape of an incremental tick: one receipt or one batch of evidence arrived.
func dirtyOnePackage(store *serverstore.Fake, purl, symbol string) {
	store.ChangedSinceFn = func(context.Context, time.Time) (serverstore.Changes, error) {
		return serverstore.Changes{
			Targets:     []serverstore.SnapshotTarget{{PURL: purl, Symbol: symbol}},
			SamplePURLs: []string{purl},
		}, nil
	}
}

// A pass with one dirty package still read a package row for every package in
// the corpus and a job history for every verified Java sample in it. Neither
// number has anything to do with what changed, and production v0.1.147 paid
// them every five minutes: active-builder pool_busy=143, query_timeout=7, a
// maximum wait for a connection of 16.357s.
//
// This pins both halves of the claim. The bulk store and the row-at-a-time
// store are seeded identically, run the identical full pass and the identical
// incremental pass, and must publish byte-identical snapshots, shards,
// clusters, receipt-derived package rows and matrix jobs -- while the bulk
// store reads a bounded number of pages instead of a number that grows with
// the corpus.
func TestIncrementalPassReadsWholeCorpusPackagesAndJobsInBoundedPages(t *testing.T) {
	const npm, maven = 40, 25
	ctx := context.Background()

	rowFake := serverstore.NewFake()
	corpus := seedBulkCorpus(t, rowFake, npm, maven)
	rowCounter := newReadCounter(rowFake)
	rowBuilder := &Builder{Store: &rowAtATimeStore{rowCounter}, Now: func() time.Time { return testNow }}

	bulkFake := serverstore.NewFake()
	if got := seedBulkCorpus(t, bulkFake, npm, maven); fmt.Sprint(got) != fmt.Sprint(corpus) {
		t.Fatal("the two halves were not seeded identically")
	}
	bulkCounter := newReadCounter(bulkFake)
	bulkBuilder := &Builder{Store: &bulkReadStore{bulkCounter}, Now: func() time.Time { return testNow }}

	for _, b := range []*Builder{rowBuilder, bulkBuilder} {
		if err := b.RunOnce(ctx); err != nil {
			t.Fatalf("full pass: %v", err)
		}
	}
	if got, want := materializedState(t, bulkFake, corpus), materializedState(t, rowFake, corpus); got != want {
		t.Fatal("bounded-page reads changed what the FULL pass published")
	}

	// One package moved. Everything else in the corpus is untouched.
	dirty, dirtySymbol := "pkg:npm/npmpkg007@1.1.0", "npmpkg007.run"
	dirtyOnePackage(rowFake, dirty, dirtySymbol)
	dirtyOnePackage(bulkFake, dirty, dirtySymbol)
	rowCounter.reset()
	bulkCounter.reset()
	for _, b := range []*Builder{rowBuilder, bulkBuilder} {
		if err := b.RunOnce(ctx); err != nil {
			t.Fatalf("incremental pass: %v", err)
		}
	}
	if got, want := materializedState(t, bulkFake, corpus), materializedState(t, rowFake, corpus); got != want {
		t.Fatal("bounded-page reads changed what the INCREMENTAL pass published")
	}

	// Evidence stays scoped to the changed package, but the production store
	// shares one checkout across the bounded batch.
	if got := rowCounter.count("EvidenceForTarget"); got > 4 {
		t.Fatalf("evidence reads for one dirty package = %d; the pass is no longer scoped", got)
	}
	if got := bulkCounter.count("EvidenceForTarget"); got != 0 {
		t.Fatalf("bulk store still read %d evidence targets one at a time", got)
	}
	if got, want := bulkCounter.count("EvidenceForTargets"), 1; got != want {
		t.Fatalf("bounded evidence pages = %d, want %d", got, want)
	}
	for _, size := range bulkCounter.sizes("EvidenceForTargets") {
		if size > targetEvidenceReadBatch {
			t.Fatalf("evidence page size = %d, max %d", size, targetEvidenceReadBatch)
		}
	}

	t.Logf("incremental pass, 1 of %d packages dirty: row-at-a-time reads "+
		"package=%d job=%d evidence=%d; bounded pages package=%d job=%d evidence=%d",
		npm+maven, rowCounter.count("GetPackage"), rowCounter.count("JobsForSample"),
		rowCounter.count("EvidenceForTarget"), bulkCounter.count("ExistingPackagePURLs"),
		bulkCounter.count("JobsForSamples"), bulkCounter.count("EvidenceForTargets"))

	// The corpus-sized reads. One receipt-resolved npm package per npm sample
	// and one maven package per maven sample, none of which changed.
	if got, want := rowCounter.count("GetPackage"), npm+maven; got != want {
		t.Fatalf("row-at-a-time package reads = %d, want %d", got, want)
	}
	if got, want := rowCounter.count("JobsForSample"), maven; got != want {
		t.Fatalf("row-at-a-time job reads = %d, want %d", got, want)
	}
	if got := bulkCounter.count("GetPackage"); got != 0 {
		t.Fatalf("bulk store still read %d package rows one at a time", got)
	}
	if got := bulkCounter.count("JobsForSample"); got != 0 {
		t.Fatalf("bulk store still read %d job histories one at a time", got)
	}
	if got, want := bulkCounter.count("ExistingPackagePURLs"), 1; got != want {
		t.Fatalf("package existence pages = %d, want %d", got, want)
	}
	if got, want := bulkCounter.count("JobsForSamples"), 1; got != want {
		t.Fatalf("job history pages = %d, want %d", got, want)
	}
}

// The pages are BOUNDED. Replacing a queue of small reads with one unbounded
// array parameter is not the fix; it is the same amount of work handed to the
// database in one breath.
func TestBulkPackageAndJobReadsArePagedRatherThanUnbounded(t *testing.T) {
	ctx := context.Background()
	const n = 2*packageProbeBatch + 500

	fake := serverstore.NewFake()
	counter := newReadCounter(fake)
	b := &Builder{Store: &bulkReadStore{counter}}

	resolved := make([]domain.PURL, 0, n)
	eligible := make([]*sampleData, 0, n)
	for i := 0; i < n; i++ {
		p, err := domain.ParsePURL(fmt.Sprintf("pkg:npm/paged%05d@1.0.0", i))
		if err != nil {
			t.Fatal(err)
		}
		resolved = append(resolved, p)
		eligible = append(eligible, &sampleData{
			row: serverstore.SampleRow{SampleID: fmt.Sprintf("sha256:%064x", 300000+i)},
		})
	}

	if _, err := b.knownPackages(ctx, resolved); err != nil {
		t.Fatalf("knownPackages: %v", err)
	}
	if got, want := counter.sizes("ExistingPackagePURLs"), []int{packageProbeBatch, packageProbeBatch, 500}; !equalInts(got, want) {
		t.Fatalf("package existence pages = %v, want %v", got, want)
	}

	if _, err := b.jobsForSamples(ctx, eligible); err != nil {
		t.Fatalf("jobsForSamples: %v", err)
	}
	if got, want := counter.sizes("JobsForSamples"), []int{jobProbeBatch, jobProbeBatch, 500}; !equalInts(got, want) {
		t.Fatalf("job history pages = %v, want %v", got, want)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// failingBulkStore refuses one of the two bulk reads. Treating an
// unanswerable existence question as "not known" would register the whole
// corpus again on every pass; treating an unanswerable job history as "no
// jobs" would open a duplicate matrix cell for every verified Java sample.
type failingBulkStore struct {
	*serverstore.Fake
	failPackages bool
	failJobs     bool
}

func (s *failingBulkStore) ExistingPackagePURLs(context.Context, []string) (map[string]bool, error) {
	if s.failPackages {
		return nil, errors.New("pool exhausted")
	}
	return map[string]bool{}, nil
}

func (s *failingBulkStore) JobsForSamples(context.Context, []string) (map[string][]serverstore.JobRow, error) {
	if s.failJobs {
		return nil, errors.New("pool exhausted")
	}
	return map[string][]serverstore.JobRow{}, nil
}

func TestBulkReadFailuresFailThePassClosed(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name         string
		failPackages bool
		failJobs     bool
		want         string
	}{
		{name: "packages", failPackages: true, want: "existing packages"},
		{name: "jobs", failJobs: true, want: "jobs for sample page"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := serverstore.NewFake()
			seedBulkCorpus(t, fake, 1, 1)
			store := &failingBulkStore{Fake: fake, failPackages: tc.failPackages, failJobs: tc.failJobs}
			err := (&Builder{Store: store, Now: func() time.Time { return testNow }}).RunOnce(ctx)
			if err == nil {
				t.Fatal("the pass reported success over a failed bulk read")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to name %q", err, tc.want)
			}
		})
	}
}
