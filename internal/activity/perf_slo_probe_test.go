package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// perfSLOProbe reads the User-Agent and the configured paths from the #511
// performance SLO probe itself, so a change to either is tested here.
func perfSLOProbe(t *testing.T) (string, []string) {
	t.Helper()
	scripts := filepath.Join("..", "..", "scripts")
	source, err := os.ReadFile(filepath.Join(scripts, "perf-slo.py"))
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^USER_AGENT = "([^"]+)"\r?$`).FindSubmatch(source)
	if match == nil {
		t.Fatal(`scripts/perf-slo.py has no USER_AGENT = "..." line`)
	}
	raw, err := os.ReadFile(filepath.Join(scripts, "perf-slo-baseline.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Paths []struct {
			Path string `json:"path"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, p := range cfg.Paths {
		paths = append(paths, p.Path)
	}
	if len(paths) == 0 {
		t.Fatal("perf-slo-baseline.json configures no paths")
	}
	return string(match[1]), paths
}

// rowsRecorded drives every path through a real Tracker with userAgent and
// returns the activity rows that reached the store after a draining Close.
func rowsRecorded(t *testing.T, userAgent string, paths []string) int {
	t.Helper()
	store := &memoryStore{recorded: make(chan struct{}, 1)}
	tracker := New(context.Background(), store, Config{
		HashKeyHex: testKey, QueueSize: 64, BatchSize: 1, FlushEvery: time.Hour,
		Now: func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) },
	})
	h := tracker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, path := range paths {
		r := httptest.NewRequest(http.MethodGet, "http://example.test"+path, nil)
		r.RemoteAddr = "198.51.100.7:443"
		r.Header.Set("User-Agent", userAgent)
		h.ServeHTTP(httptest.NewRecorder(), r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := tracker.Close(ctx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.rows)
}

// The #511 probe runs after every deploy and daily against production. It
// must never write activity rows or count toward the active-network KPI.
func TestPerfSLOProbeIsNeverRecordedAsActivity(t *testing.T) {
	userAgent, paths := perfSLOProbe(t)
	if !likelyAutomated(userAgent) {
		t.Fatalf("probe User-Agent %q is not classified as automated", userAgent)
	}
	for _, path := range paths {
		if n := rowsRecorded(t, userAgent, []string{path}); n != 0 {
			t.Errorf("probe GET %s recorded %d activity rows, want 0", path, n)
		}
	}
	// Control: the same paths from a real client are recorded, so the zero
	// above is the User-Agent's doing and not an untracked route set.
	if n := rowsRecorded(t, "codesamplex-cli/1.0", paths); n == 0 {
		t.Fatal("control: a client GET of the probe paths recorded nothing; the test no longer exercises a tracked route")
	}
}
