package search

// SyncAll says how far it is, so a caller waiting minutes can say so too.
//
// On the reporting workstation (2026-09-03, a 246MB local DB) `csx sync`
// warmed 1,558 shard keys in about fifteen minutes and printed nothing
// until the end. The loop knew exactly where it was the whole time; it
// simply told nobody. Progress is an optional callback -- nil is the
// existing behaviour -- called once per key with the count so far and the
// total, so the daemon can publish it and the CLI can render it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func testDB(t *testing.T) *localdb.DB {
	t.Helper()
	db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSyncAllReportsProgressPerKey(t *testing.T) {
	// A server with no shards: every key is absent, which SyncAll treats as
	// "nothing to warm" -- the progress callback must still fire per key,
	// because what the caller is waiting on is the walk, not the hits.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	var seen [][2]int
	s := &Syncer{DB: testDB(t), HTTP: srv.Client(), ServerURL: srv.URL, Progress: func(done, total int) {
		seen = append(seen, [2]int{done, total})
	}}
	keys := []string{"npm/a/0", "npm/b/0", "npm/c/0"}
	_, _ = s.SyncAll(context.Background(), keys) // outcome is not what is asserted; the walk is
	if len(seen) != len(keys) {
		t.Fatalf("progress reported %d times for %d keys: %v", len(seen), len(keys), seen)
	}
	for i, p := range seen {
		if p[0] != i+1 || p[1] != len(keys) {
			t.Fatalf("progress[%d] = %v, want {%d %d}", i, p, i+1, len(keys))
		}
	}
}

func TestSyncAllWithoutProgressIsUnchanged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	s := &Syncer{DB: testDB(t), HTTP: srv.Client(), ServerURL: srv.URL}
	_, _ = s.SyncAll(context.Background(), []string{"npm/a/0"}) // must not panic on a nil Progress
}
