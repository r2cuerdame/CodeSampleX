package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type fakeBlobStore struct {
	blobs map[string][]byte
	gets  atomic.Int64
	delay time.Duration
}

func (f *fakeBlobStore) Put(context.Context, io.Reader) (string, error) { panic("unused") }
func (f *fakeBlobStore) Get(ctx context.Context, id string) (io.ReadCloser, error) {
	f.gets.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	b, ok := f.blobs[id]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeBlobStore) Has(context.Context, string) (bool, error) { panic("unused") }
func (f *fakeBlobStore) Delete(context.Context, string) error      { panic("unused") }
func (f *fakeBlobStore) TotalSize(context.Context) (int64, error)  { panic("unused") }

func testTgzOf(t *testing.T, files [][2]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name:     f[0],
			Mode:     0o644,
			Size:     int64(len(f[1])),
			Typeflag: tar.TypeReg,
			Format:   tar.FormatUSTAR,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(f[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSampleMetaAndSourceShareArtifactDecode(t *testing.T) {
	ctx := context.Background()
	store := serverstore.NewFake()
	sampleID := "sha256:1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	tgz := testTgzOf(t, [][2]string{
		{"README.md", "# Hello"},
		{"main.go", "package main\n\nfunc main() {}\n"},
	})
	blobs := &fakeBlobStore{blobs: map[string][]byte{sampleID: tgz}}
	if err := store.SaveSample(ctx, serverstore.SampleRow{
		SampleID:     sampleID,
		Status:       "PUBLISHED",
		License:      "MIT",
		OriginSeeder: "tester",
		CreatedAt:    time.Now(),
		ManifestJSON: `{"goal":"test sharing"}`,
	}); err != nil {
		t.Fatal(err)
	}
	w := &webStore{s: store, blobs: blobs}

	meta, ok, err := w.SampleMeta(ctx, sampleID)
	if err != nil || !ok {
		t.Fatalf("SampleMeta err=%v ok=%v", err, ok)
	}
	if len(meta.Files) != 2 || meta.Files[0] != "README.md" || meta.Files[1] != "main.go" {
		t.Fatalf("unexpected meta.Files: %v", meta.Files)
	}
	source, err := w.SampleSource(ctx, sampleID)
	if err != nil {
		t.Fatalf("SampleSource err=%v", err)
	}
	if len(source) != 2 || source[0].Name != "README.md" || source[1].Name != "main.go" {
		t.Fatalf("unexpected source files: %+v", source)
	}
	if got := blobs.gets.Load(); got != 1 {
		t.Fatalf("blob gets = %d, want 1 (shared decode between SampleMeta and SampleSource)", got)
	}
}

type detailBurstStore struct {
	*serverstore.Fake
	pkgVersionsCalls atomic.Int64
	pkgSamplesCalls  atomic.Int64
	pkgCountsCalls   atomic.Int64
	wantedCalls      atomic.Int64
	clustersCalls    atomic.Int64
	depsCalls        atomic.Int64
	sampleCalls      atomic.Int64
	receiptsCalls    atomic.Int64
}

func (s *detailBurstStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	s.pkgVersionsCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.PackageRow{{
		PURL:      "pkg:npm/burstpkg@1.0.0",
		Ecosystem: ecosystem,
		Name:      name,
		Version:   "1.0.0",
	}}, nil
}

func (s *detailBurstStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	return []serverstore.SnapshotTarget{{
		PURL:   "pkg:npm/burstpkg@1.0.0",
		Symbol: "testSym",
	}}, nil
}

func (s *detailBurstStore) VerifiedSamplesForPackages(ctx context.Context, purls []string, limit int) ([]serverstore.SampleRow, error) {
	s.pkgSamplesCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.SampleRow{{
		SampleID:     "sha256:samplesample",
		ManifestJSON: `{"goal":"test","packages":["pkg:npm/burstpkg@1.0.0"]}`,
		Status:       "PUBLISHED",
	}}, nil
}

func (s *detailBurstStore) VerifiedSampleCodeCounts(ctx context.Context, purl string) ([]serverstore.VerifiedSampleCodeCount, error) {
	s.pkgCountsCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.VerifiedSampleCodeCount{{
		PURL:    "pkg:npm/burstpkg@1.0.0",
		Symbol:  "testSym",
		Samples: 5,
	}}, nil
}

func (s *detailBurstStore) WantedForPackage(ctx context.Context, ecosystem, name string) ([]serverstore.WantedRow, error) {
	s.wantedCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.WantedRow{{
		Ecosystem: ecosystem,
		Name:      name,
		Version:   "1.0.0",
		Symbol:    "testSym",
		Asks:      3,
	}}, nil
}

func (s *detailBurstStore) ListFailureClusters(ctx context.Context, packageName string) ([]serverstore.ClusterRow, error) {
	s.clustersCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.ClusterRow{{
		Ecosystem:        "npm",
		PackageName:      packageName,
		Symbol:           "testSym",
		Stage:            "test",
		ErrorFingerprint: "sha256:fp",
		ObservationCount: 2,
	}}, nil
}

func (s *detailBurstStore) Dependencies(ctx context.Context, ecosystem, name string) ([]serverstore.DependencyEdge, error) {
	s.depsCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.DependencyEdge{{
		ParentName:    name,
		ParentVersion: "1.0.0",
		ChildName:     "depA",
		ChildVersion:  "2.0.0",
		Projects:      4,
	}}, nil
}

func (s *detailBurstStore) GetSample(ctx context.Context, sampleID string) (serverstore.SampleRow, bool, error) {
	s.sampleCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return serverstore.SampleRow{
		SampleID:     sampleID,
		Status:       "PUBLISHED",
		ManifestJSON: `{"goal":"test"}`,
		CreatedAt:    time.Now(),
	}, true, nil
}

func (s *detailBurstStore) ReceiptsForSample(ctx context.Context, sampleID string) ([]serverstore.ReceiptRow, error) {
	s.receiptsCalls.Add(1)
	time.Sleep(20 * time.Millisecond)
	return []serverstore.ReceiptRow{{
		SampleID:    sampleID,
		ReceiptJSON: `{"receipt":"pass"}`,
	}}, nil
}

func TestConcurrentDetailBurstCoalescesSingleExecution(t *testing.T) {
	store := &detailBurstStore{Fake: serverstore.NewFake()}
	sampleID := "sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd"
	tgz := testTgzOf(t, [][2]string{{"main.go", "package main"}})
	blobs := &fakeBlobStore{blobs: map[string][]byte{sampleID: tgz}, delay: 20 * time.Millisecond}
	w := &webStore{s: store, blobs: blobs}
	const burst = 8

	// 1. PackageVersions
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vers, err := w.PackageVersions(context.Background(), "npm", "burstpkg")
			if err != nil || len(vers) != 1 {
				t.Errorf("PackageVersions = %v, err=%v", vers, err)
			}
		}()
	}
	wg.Wait()
	if got := store.pkgVersionsCalls.Load(); got != 1 {
		t.Errorf("PackageVersions calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 2. PackageSamples
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := w.PackageSamples(context.Background(), "npm", "burstpkg", 50)
			if err != nil || len(items) != 1 {
				t.Errorf("PackageSamples = %v, err=%v", items, err)
			}
		}()
	}
	wg.Wait()
	if got := store.pkgSamplesCalls.Load(); got != 1 {
		t.Errorf("PackageSamples calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 3. PackageCodeCounts
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			counts, err := w.PackageCodeCounts(context.Background(), "npm", "burstpkg")
			if err != nil || len(counts) != 1 {
				t.Errorf("PackageCodeCounts = %v, err=%v", counts, err)
			}
		}()
	}
	wg.Wait()
	if got := store.pkgCountsCalls.Load(); got != 1 {
		t.Errorf("PackageCodeCounts calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 4. WantedForPackage
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wanted, err := w.WantedForPackage(context.Background(), "npm", "burstpkg")
			if err != nil || len(wanted) != 1 {
				t.Errorf("WantedForPackage = %v, err=%v", wanted, err)
			}
		}()
	}
	wg.Wait()
	if got := store.wantedCalls.Load(); got != 1 {
		t.Errorf("WantedForPackage calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 5. FailureClusters
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			docs, matched, err := w.FailureClusters(context.Background(), "npm", "burstpkg")
			if err != nil || len(docs) != 1 || matched != 1 {
				t.Errorf("FailureClusters = %d docs, matched=%d, err=%v", len(docs), matched, err)
			}
		}()
	}
	wg.Wait()
	if got := store.clustersCalls.Load(); got != 1 {
		t.Errorf("FailureClusters calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 6. Dependencies
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			edges, err := w.Dependencies(context.Background(), "npm", "burstpkg")
			if err != nil || len(edges) != 1 {
				t.Errorf("Dependencies = %v, err=%v", edges, err)
			}
		}()
	}
	wg.Wait()
	if got := store.depsCalls.Load(); got != 1 {
		t.Errorf("Dependencies calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 7. SampleMeta
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			meta, ok, err := w.SampleMeta(context.Background(), sampleID)
			if err != nil || !ok || meta.SampleID != sampleID {
				t.Errorf("SampleMeta = %v, ok=%v, err=%v", meta, ok, err)
			}
		}()
	}
	wg.Wait()
	if got := store.sampleCalls.Load(); got != 1 {
		t.Errorf("SampleMeta calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 8. SampleReceipts
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipts, err := w.SampleReceipts(context.Background(), sampleID)
			if err != nil || len(receipts) != 1 {
				t.Errorf("SampleReceipts = %v, err=%v", receipts, err)
			}
		}()
	}
	wg.Wait()
	if got := store.receiptsCalls.Load(); got != 1 {
		t.Errorf("SampleReceipts calls = %d, want 1 coalesced call for burst of %d", got, burst)
	}

	// 9. SampleSource burst against blob store
	sampleID2 := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	blobs.blobs[sampleID2] = tgz
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			files, err := w.SampleSource(context.Background(), sampleID2)
			if err != nil || len(files) != 1 {
				t.Errorf("SampleSource = %v, err=%v", files, err)
			}
		}()
	}
	wg.Wait()
	// Note: sampleID already loaded blobs once during SampleMeta. sampleID2 should load exactly once for all 8 callers.
	// Total blob gets should be 1 (from sampleID) + 1 (from sampleID2 burst) = 2.
	if got := blobs.gets.Load(); got != 2 {
		t.Errorf("SampleSource blob gets = %d, want 2", got)
	}
}

func TestSingleflightLeaderCancellationSafety(t *testing.T) {
	group := singleflightGroup[string]{}
	started := make(chan struct{})
	release := make(chan struct{})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)

	go func() {
		val, err := group.Do(leaderCtx, "testKey", func(ctx context.Context) (string, error) {
			close(started)
			select {
			case <-release:
				return "successValue", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})
		_ = val
		leaderDone <- err
	}()

	<-started

	waiterDone := make(chan struct {
		val string
		err error
	}, 1)

	go func() {
		val, err := group.Do(context.Background(), "testKey", func(ctx context.Context) (string, error) {
			return "waiterCalledDirectly", nil
		})
		waiterDone <- struct {
			val string
			err error
		}{val: val, err: err}
	}()

	// Waiter is parked waiting for leader's shared call.
	// Now cancel leader context.
	cancelLeader()

	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("leader err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("leader did not finish after cancellation")
	}

	// Ensure waiter did NOT abort prematurely with context.Canceled!
	select {
	case res := <-waiterDone:
		t.Fatalf("waiter finished prematurely before load released: %+v", res)
	case <-time.After(30 * time.Millisecond):
	}

	// Release the in-flight function.
	close(release)

	select {
	case res := <-waiterDone:
		if res.err != nil {
			t.Fatalf("waiter failed with error: %v", res.err)
		}
		if res.val != "successValue" {
			t.Fatalf("waiter val = %q, want successValue", res.val)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter timed out waiting for shared result")
	}
}

type failThenSucceedStore struct {
	*serverstore.Fake
	attempts atomic.Int64
}

func (s *failThenSucceedStore) ListPackageVersions(ctx context.Context, ecosystem, name string) ([]serverstore.PackageRow, error) {
	if s.attempts.Add(1) == 1 {
		return nil, serverstore.ErrPoolBusy
	}
	return []serverstore.PackageRow{{
		PURL:      "pkg:npm/retrypkg@1.0.0",
		Ecosystem: ecosystem,
		Name:      name,
		Version:   "1.0.0",
	}}, nil
}

func (s *failThenSucceedStore) SnapshotKeys(ctx context.Context) ([]serverstore.SnapshotTarget, error) {
	return []serverstore.SnapshotTarget{{
		PURL:   "pkg:npm/retrypkg@1.0.0",
		Symbol: "testSym",
	}}, nil
}

func TestSingleflightFailureDoesNotPoisonSubsequentRetry(t *testing.T) {
	store := &failThenSucceedStore{Fake: serverstore.NewFake()}
	w := &webStore{s: store}

	// Attempt 1: should fail with ErrPoolBusy.
	_, err := w.PackageVersions(context.Background(), "npm", "retrypkg")
	if !errors.Is(err, serverstore.ErrPoolBusy) {
		t.Fatalf("first attempt err = %v, want ErrPoolBusy", err)
	}

	// Attempt 2: should retry and succeed, NOT return cached failure.
	vers, err := w.PackageVersions(context.Background(), "npm", "retrypkg")
	if err != nil {
		t.Fatalf("second attempt err = %v, want nil", err)
	}
	if len(vers) != 1 || vers[0] != "1.0.0" {
		t.Fatalf("second attempt versions = %v, want [1.0.0]", vers)
	}
	if got := store.attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2 (failure not cached)", got)
	}
}
