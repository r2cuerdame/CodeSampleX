package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheContractFixture(t *testing.T) (*Daemon, string) {
	t.Helper()
	home := newTestHome(t, nil)
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.DB.SetStat(t.Context(), statEvidenceSent, "37"); err != nil {
		t.Fatal(err)
	}
	if _, err = d.DB.Enqueue(t.Context(), "adoption", "retained-report"); err != nil {
		t.Fatal(err)
	}
	return d, home
}
func assertCacheUnknownAndCore(t *testing.T, st Stats, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("core stats blocked by cache accounting: %v", err)
	}
	if st.EvidenceBatchesSent != 37 || st.Queue.Uploads != 1 || st.QueueDepth != 1 {
		t.Fatalf("core facts changed: accepted=%d queue=%+v", st.EvidenceBatchesSent, st.Queue)
	}
	raw, e := json.Marshal(st)
	if e != nil {
		t.Fatal(e)
	}
	var m map[string]any
	if e = json.Unmarshal(raw, &m); e != nil {
		t.Fatal(e)
	}
	v, ok := m["cacheBytes"]
	if !ok || v != nil {
		t.Fatalf("unavailable cache must be explicit null, got %v", v)
	}
	q := m["retrievalQuality"].(map[string]any)
	if v, ok = q["cacheBytes"]; !ok || v != nil {
		t.Fatalf("nested cache must remain unknown, got %v", v)
	}
	var decoded Stats
	if e = json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	again, e := json.Marshal(decoded)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(again, []byte("\"cacheBytes\":null")) {
		t.Fatal("daemon client round trip turned unknown into zero")
	}
}
func TestStatsCoreSurvivesInvalidCASFileState(t *testing.T) {
	d, home := cacheContractFixture(t)
	d.Close()
	path := filepath.Join(home, "cas", "sha256")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid CAS root"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := StatsFromDisk(t.Context(), home)
	assertCacheUnknownAndCore(t, st, err)
}

type cancelCASWriter struct {
	cancel context.CancelFunc
	out    bytes.Buffer
}

func (w *cancelCASWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "stage=cas_size state=begin") {
		w.cancel()
	}
	return w.out.Write(p)
}
func TestStatsCoreSnapshotPrecedesCanceledWideCASWalk(t *testing.T) {
	d, home := cacheContractFixture(t)
	defer d.Close()
	for i := 0; i < 4096; i++ {
		dir := filepath.Join(home, "cas", "sha256", fmt.Sprintf("%02x", i/256), fmt.Sprintf("%02x", i%256))
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%064x", i)), []byte("cache object"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &cancelCASWriter{cancel: cancel}
	ctx = WithStatsDiagnostics(ctx, writer)
	start := time.Now()
	st, err := d.StatsNow(ctx)
	elapsed := time.Since(start)
	assertCacheUnknownAndCore(t, st, err)
	if elapsed > time.Second {
		t.Fatalf("canceled optional cache delayed snapshot %s", elapsed)
	}
	if !strings.Contains(writer.out.String(), "stage=cas_size state=failed") {
		t.Fatal("incomplete walk not recorded as failed")
	}
	if _, err := os.Stat(filepath.Join(home, "cas", "sha256", "0f", "ff", fmt.Sprintf("%064x", 4095))); err != nil {
		t.Fatal("cache file changed")
	}
	t.Logf("actual fixture: 4096 leaf dirs, 4096 files; core response=%s; files retained", elapsed)
}
