package web

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

type versionSnapshotRecordingStore struct {
	*fakeStore
	mu    sync.Mutex
	reads []string
}

func (s *versionSnapshotRecordingStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	s.mu.Lock()
	s.reads = append(s.reads, snapKey(purl, symbol))
	s.mu.Unlock()
	return s.fakeStore.SnapshotJSON(ctx, purl, symbol)
}

func (s *versionSnapshotRecordingStore) recordedReads() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...)
}

func TestVersionPageReadsOnlyTheRequestedReleaseSnapshots(t *testing.T) {
	store := &versionSnapshotRecordingStore{fakeStore: newCubeStore()}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })
	res := get(t, mux, "/npm/reactish/19.1.0")
	if res.Code != 200 {
		t.Fatalf("status = %d", res.Code)
	}
	reads := store.recordedReads()
	for _, key := range reads {
		if strings.Contains(key, "@18.3.1") {
			t.Fatalf("version route read sibling release snapshot %q", key)
		}
	}
	if got, want := len(reads), 3; got != want {
		t.Fatalf("snapshot reads = %d, want package + two symbols: %v", got, reads)
	}
}

type activeBackgroundSnapshotStore struct {
	*versionSnapshotRecordingStore
	startedOnce sync.Once
	started     chan struct{}
	release     chan struct{}
	blockedPURL string
}

func (s *activeBackgroundSnapshotStore) SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool) {
	if purl == s.blockedPURL {
		s.startedOnce.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return "", false
		}
	}
	return s.versionSnapshotRecordingStore.SnapshotJSON(ctx, purl, symbol)
}

func TestVersionRouteDoesNotJoinSiblingBackgroundWork(t *testing.T) {
	store := &activeBackgroundSnapshotStore{
		versionSnapshotRecordingStore: &versionSnapshotRecordingStore{fakeStore: newCubeStore()},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
		blockedPURL:                   "pkg:npm/reactish@18.3.1",
	}
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = store })
	backgroundDone := make(chan int, 1)
	go func() {
		// Exercise the real package cube loader and its package-keyed
		// singleflight. The version route must not join that package-wide work
		// merely because an older sibling release is still being assembled.
		backgroundDone <- get(t, mux, "/npm/reactish").Code
	}()
	defer func() {
		close(store.release)
		<-backgroundDone
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("background snapshot work did not start")
	}

	done := make(chan int, 1)
	go func() { done <- get(t, mux, "/npm/reactish/19.1.0").Code }()
	select {
	case status := <-done:
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("version route waited for sibling background work")
	}
}

// The version page used to answer "which symbol ran where" with a symbol-by-OS
// grid. In production every symbol-grain fact is a contract receipt and every
// receipt is signed in a linux container, so that grid could only ever draw
// one column — and it read as "these APIs run on linux and nowhere else".
//
// The grid is gone. The evidence it was carrying belongs on the symbol row,
// where it is about the API and the release and not about an OS.
func TestASymbolRowSaysWhatThisNetworkRanForIt(t *testing.T) {
	f := newCubeStore()
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/reactish/19.1.0").Body.String()

	i := strings.Index(body, `<ul class="symlist`)
	if i < 0 {
		t.Fatal("no symbol list on the version page")
	}
	list := body[i : i+strings.Index(body[i:], "</ul>")]
	if !strings.Contains(list, "symruns") {
		t.Errorf("no symbol row states what this network ran:\n%s", list)
	}
	// hydrateRoot: two contract runs on this release, both failing.
	if !strings.Contains(list, "0 of 2 runs passed") {
		t.Errorf("the counts do not match the receipts:\n%s", list)
	}
}

// An API this network never ran states nothing beyond its name — no zero, no
// implied absence of evidence elsewhere.
func TestASymbolWithNoRunsClaimsNothing(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|quiet"] = []string{"1.0.0"}
	f.symbols["npm|quiet|1.0.0"] = []string{"onlyNamed"}
	f.snapshots[snapKey("pkg:npm/quiet@1.0.0", "")] =
		cubeSnap("pkg:npm/quiet@1.0.0", "", "linux", "x64", "node", "22.1", "npm", "PROJECT_COMPILE", 3, 0)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/quiet/1.0.0").Body.String()

	if strings.Contains(body, "0 of 0 runs") {
		t.Error("a row invented a count for an API nothing ran")
	}
}

// An observation is recorded against the PACKAGE, not the API. Counting it
// per symbol would put a package's builds behind every symbol name it
// happens to mention — the same mistake that gave commons-logging a page for
// a Spring Test class.
func TestPackageObservationsAreNotCountedAsSymbolRuns(t *testing.T) {
	f := newFakeStore()
	f.versions["npm|obs"] = []string{"1.0.0"}
	f.symbols["npm|obs|1.0.0"] = []string{"someCall"}
	f.snapshots[snapKey("pkg:npm/obs@1.0.0", "")] =
		cubeSnap("pkg:npm/obs@1.0.0", "", "linux", "x64", "node", "22.1", "npm", "PROJECT_COMPILE", 40, 0)
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/npm/obs/1.0.0").Body.String()

	if strings.Contains(body, "40") && strings.Contains(body, "symruns") {
		t.Error("the package's 40 builds were credited to a symbol row")
	}
}
