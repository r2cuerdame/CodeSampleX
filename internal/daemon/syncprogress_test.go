package daemon

// The daemon publishes where a sync is, so a client waiting on
// POST /local/v1/sync can ask GET /local/v1/status and render it.
//
// A sync on the reporting workstation takes about fifteen minutes (246MB
// local DB, 1,558 shard keys). The only things a caller could observe
// during it were a silent connection and, at 30 seconds, a client timeout.
// Progress is a small struct behind a mutex: the stage the daemon is in,
// how far through it, and when it started. It is nil when nothing is
// syncing, so status output for an idle daemon does not change.

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSyncProgressIsPublishedWhileSyncingAndClearedAfter(t *testing.T) {
	d := &Daemon{}
	if got := d.syncProgress(); got != nil {
		t.Fatalf("idle daemon reports progress %+v, want none", got)
	}

	started := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	d.beginSyncStage("warming", 1558, started)
	d.advanceSync(312)
	got := d.syncProgress()
	if got == nil {
		t.Fatal("no progress while warming")
	}
	if got.Stage != "warming" || got.Done != 312 || got.Total != 1558 || !got.StartedAt.Equal(started) {
		t.Fatalf("progress = %+v", got)
	}

	d.beginSyncStage("uploading", 4, started)
	got = d.syncProgress()
	if got.Stage != "uploading" || got.Done != 0 || got.Total != 4 {
		t.Fatalf("a new stage starts from zero: %+v", got)
	}

	d.endSync()
	if got := d.syncProgress(); got != nil {
		t.Fatalf("progress survived endSync: %+v", got)
	}
}

// The wire shape: status carries `sync` only while one is running.
func TestStatusInfoCarriesSyncOnlyWhileRunning(t *testing.T) {
	idle, err := json.Marshal(StatusInfo{SchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(idle) == "" || hasSub(string(idle), `"sync"`) {
		t.Fatalf("idle status must not carry a sync field: %s", idle)
	}
	busy, err := json.Marshal(StatusInfo{SchemaVersion: 1, Sync: &SyncProgress{Stage: "warming", Done: 1, Total: 3}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sync"`, `"stage":"warming"`, `"done":1`, `"total":3`} {
		if !hasSub(string(busy), want) {
			t.Fatalf("busy status lacks %s: %s", want, busy)
		}
	}
}

func hasSub(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
