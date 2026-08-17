package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// unreachableServer is a local address nothing listens on: connection
// attempts fail immediately without any real network traffic.
const unreachableServer = "http://127.0.0.1:1"

// newTestHome writes a home with an ephemeral daemon port (never a fixed
// port — tests run in parallel) and a server URL that cannot be reached.
func newTestHome(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	home := t.TempDir()
	cfg := config.Default()
	cfg.Mode = config.ModeLocalOnly
	cfg.DaemonPort = 0
	cfg.ServerURL = unreachableServer
	if mutate != nil {
		mutate(cfg)
	}
	if err := cfg.Save(home); err != nil {
		t.Fatalf("save config: %v", err)
	}
	return home
}

// startDaemon runs a daemon for home and returns it with a TCP client.
// Shutdown and store cleanup are registered on t.
func startDaemon(t *testing.T, home string) (*Daemon, *Client) {
	t.Helper()
	d, err := New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errCh:
		cancel()
		t.Fatalf("daemon.Run exited early: %v", err)
	case <-time.After(15 * time.Second):
		cancel()
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("daemon did not shut down")
		}
		d.Close()
	})
	return d, &Client{BaseURL: d.BaseURL()}
}

// TestIdleVerificationStartsPromptly pins the behavior a cross-verifying
// peer depends on: with a budget configured, the daemon asks the server for
// verification jobs shortly after startup instead of after a full 15-minute
// tick — which is what kept published samples stuck at PUBLISHED.
func TestIdleVerificationStartsPromptly(t *testing.T) {
	jobsAsked := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/verification/jobs") {
			select {
			case jobsAsked <- struct{}{}:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jobs":[]}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	home := newTestHome(t, func(c *config.Config) {
		c.Mode = config.ModeCommunity
		c.ServerURL = srv.URL
		c.IdleVerification = "unlimited"
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	if d.Cross == nil {
		t.Fatal("idleVerification=unlimited must wire a CrossVerifier")
	}
	d.verifyFirstDelay = 50 * time.Millisecond
	d.verifyEvery = 500 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-errCh
		d.Close()
	})

	select {
	case <-jobsAsked:
	case <-time.After(20 * time.Second):
		t.Fatal("daemon never polled /v1/verification/jobs after startup")
	}
}

func seedSample(t *testing.T, d *Daemon, id string) domain.SampleManifest {
	t.Helper()
	m := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{
			SchemaVersion: 1, Kind: "HOW",
			Goal:     "upload multipart form with axios",
			Packages: []string{"pkg:npm/axios@1.12.0"},
			Symbols:  []string{"axios.post"},
			Contract: []string{"asserts documented behavior"},
		},
		Packages:        []string{"pkg:npm/axios@1.12.0"},
		Symbols:         []string{"axios.post"},
		Environment:     testEnv(),
		License:         "MIT-0",
		ContractCommand: []string{"node", "test/contract.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
	if err := search.SeedSampleDoc(context.Background(), d.DB, m, id, "LOCAL_PASS"); err != nil {
		t.Fatalf("seed sample: %v", err)
	}
	return m
}

func testEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", OSVersionBucket: "11",
		Arch: "x64", Runtime: "node", RuntimeVersion: "22.18",
		Language: "typescript", LanguageVersion: "5.9",
		ModuleSystem: "esm", ExecutionContext: "node",
	}
}

func TestDaemonStatusSearchQueueEndpoints(t *testing.T) {
	home := newTestHome(t, nil)
	d, c := startDaemon(t, home)
	ctx := context.Background()
	seedSample(t, d, "sha256:aaa1")

	// Status.
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.Mode != config.ModeLocalOnly || st.Home != home {
		t.Errorf("status mode/home = %q/%q", st.Mode, st.Home)
	}
	if !strings.HasPrefix(st.PeerID, "ed25519:") {
		t.Errorf("status peerId = %q", st.PeerID)
	}
	if st.Version == "" || st.Uptime == "" {
		t.Errorf("status version/uptime empty: %+v", st)
	}

	// Search hits the seeded sample.
	resp, err := c.Search(ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "upload multipart form with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Environment:   testEnv(),
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if resp.Miss || len(resp.Results) == 0 {
		t.Fatalf("search miss=%v results=%d, want hit", resp.Miss, len(resp.Results))
	}
	if resp.Results[0].SampleID != "sha256:aaa1" {
		t.Errorf("top sample = %q", resp.Results[0].SampleID)
	}

	// Seed one observation and check the queue preview carries it and
	// that previewing does NOT consume it.
	env := testEnv()
	if err := d.DB.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("save env: %v", err)
	}
	key := localdb.ObsKey{
		Epoch: "2026-08-13", PURL: "pkg:npm/axios@1.12.0",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile, Result: domain.ResultPass,
	}
	if err := d.DB.RecordObservation(ctx, key, 2); err != nil {
		t.Fatalf("record observation: %v", err)
	}
	q1, err := c.Queue(ctx)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(q1.Batches) != 1 || q1.Batches[0].Package != "pkg:npm/axios@1.12.0" {
		t.Fatalf("queue batches = %+v, want the seeded observation", q1.Batches)
	}
	if q1.Batches[0].ObservationCount != 2 || q1.Batches[0].AnonID == "" {
		t.Errorf("batch fields = %+v", q1.Batches[0])
	}
	q2, err := c.Queue(ctx)
	if err != nil {
		t.Fatalf("queue again: %v", err)
	}
	if len(q2.Batches) != 1 {
		t.Fatalf("privacy preview consumed the queue: second call has %d batches", len(q2.Batches))
	}

	// Sample lookup.
	info, err := c.Sample(ctx, "sha256:aaa1")
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if info.Status != "LOCAL_PASS" || info.Manifest.Case.Goal == "" {
		t.Errorf("sample info = %+v", info)
	}
	if _, err := c.Sample(ctx, "sha256:missing"); err == nil {
		t.Error("missing sample should 404")
	}
}

// The §12.5 privacy preview must contain only sanitized fields: a
// FAIL observation seeded from a raw error full of Windows paths and a
// username may surface only as fingerprint + error code.
func TestPrivacyPreviewSanitized(t *testing.T) {
	home := newTestHome(t, nil)
	d, c := startDaemon(t, home)
	ctx := context.Background()

	raw := `Error: Cannot find module 'C:\Users\alice\secret-project\src\index.ts'
    at Object.<anonymous> (C:\Users\alice\secret-project\node_modules\axios\dist\node\axios.cjs:1417:13)
    npm ERR! code ERR_REQUIRE_ESM`
	san := sanitizer.Sanitize(raw, domain.StageProjectCompile, []string{"axios"})
	if san.Fingerprint == "" {
		t.Fatal("sanitizer produced no fingerprint")
	}

	env := testEnv()
	if err := d.DB.SaveEnvironment(ctx, env); err != nil {
		t.Fatalf("save env: %v", err)
	}
	err := d.DB.RecordObservation(ctx, localdb.ObsKey{
		Epoch: "2026-08-13", PURL: "pkg:npm/axios@1.12.0",
		EnvHash: env.Hash(), Stage: domain.StageProjectCompile,
		Result: domain.ResultFail, ErrorFP: san.Fingerprint, ErrorCode: san.Code,
	}, 1)
	if err != nil {
		t.Fatalf("record observation: %v", err)
	}

	res, err := http.Get(d.BaseURL() + "/local/v1/queue")
	if err != nil {
		t.Fatalf("GET queue: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	text := string(body)

	for _, banned := range []string{`\`, "secret-project", "alice", "Users", "index.ts", "axios.cjs"} {
		if strings.Contains(text, banned) {
			t.Errorf("privacy preview leaked %q:\n%s", banned, text)
		}
	}
	if !strings.Contains(text, san.Fingerprint) {
		t.Errorf("preview missing sanitized fingerprint %s", san.Fingerprint)
	}

	// Every batch may carry ONLY the ObservationBatch wire fields.
	var parsed struct {
		Batches []map[string]any `json:"batches"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("parse queue body: %v", err)
	}
	if len(parsed.Batches) == 0 {
		t.Fatal("no batches in preview")
	}
	allowed := map[string]bool{
		"schemaVersion": true, "epoch": true, "anonId": true, "projectBucket": true,
		"package": true, "symbol": true, "symbolConfidence": true, "environment": true,
		"stage": true, "result": true, "observationCount": true,
		"errorFingerprint": true, "errorCode": true,
	}
	for _, b := range parsed.Batches {
		for k := range b {
			if !allowed[k] {
				t.Errorf("batch carries non-contract field %q", k)
			}
		}
	}

	c2, err := c.Queue(ctx)
	if err != nil {
		t.Fatalf("typed queue: %v", err)
	}
	if len(c2.Batches) != 1 || c2.Batches[0].ErrorFingerprint != san.Fingerprint {
		t.Errorf("typed queue = %+v", c2.Batches)
	}
}

func TestAdoptionRecordsHit(t *testing.T) {
	// Community mode: an adoption report is queued for upload only where
	// the user agreed to take part. The mode used to go unstated here, and
	// the test passed against an uninitialized install that was queuing
	// uploads nobody had consented to.
	home := newTestHome(t, func(cfg *config.Config) { cfg.Mode = config.ModeCommunity })
	d, c := startDaemon(t, home)
	ctx := context.Background()
	seedSample(t, d, "sha256:bbb2")

	pass := true
	if err := c.Adopt(ctx, AdoptionRequest{SampleID: "sha256:bbb2", Applied: true, BuildPass: &pass}); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	hits, err := d.DB.ListHits(ctx, 10)
	if err != nil {
		t.Fatalf("list hits: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	h := hits[0]
	if h.SampleID != "sha256:bbb2" || !h.Adopted || !h.PostBuildPass.Valid || !h.PostBuildPass.Bool {
		t.Errorf("hit row = %+v", h)
	}

	// Adoption evidence is queued (sanitized by construction) and shows
	// up in the privacy preview.
	items, err := d.DB.QueuePending(ctx, 10)
	if err != nil {
		t.Fatalf("queue pending: %v", err)
	}
	if len(items) != 1 || items[0].Kind != "adoption" {
		t.Fatalf("queued items = %+v", items)
	}
	if strings.Contains(items[0].Payload, home) {
		t.Errorf("adoption payload leaked a path: %s", items[0].Payload)
	}
	q, err := c.Queue(ctx)
	if err != nil {
		t.Fatalf("queue: %v", err)
	}
	if len(q.Queued) != 1 || q.Queued[0].Kind != "adoption" {
		t.Errorf("preview queued = %+v", q.Queued)
	}

	// Missing sampleId is rejected.
	if err := c.Adopt(ctx, AdoptionRequest{Applied: true}); err == nil {
		t.Error("adoption without sampleId should fail")
	}
}

func TestSingleInstanceLockRefusesSecondDaemon(t *testing.T) {
	home := newTestHome(t, nil)
	startDaemon(t, home)

	d2, err := New(home)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer d2.Close()
	err = d2.Run(context.Background())
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run error = %v, want ErrAlreadyRunning", err)
	}
}

func TestSyncEndpointWarmsShardsAndToleratesOffline(t *testing.T) {
	// Fake server: stats advertises one HOT shard, and both shards exist.
	//
	// Only axios used to exist here, and the test still asserted two warmed
	// keys -- which passed only because a 404 was counted as a warm. That
	// is the exact miscount the warmed number was introduced to end, so the
	// fixture now serves what it claims the client warmed.
	shardBody := `{"schemaVersion":1,"key":"npm/axios/1","generatedAt":"2026-08-13T00:00:00Z","packages":[]}`
	hotBody := `{"schemaVersion":1,"key":"npm/left-pad/1","generatedAt":"2026-08-13T00:00:00Z","packages":[]}`
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"hotShards":["npm/left-pad/1"]}`)
	})
	mux.HandleFunc("GET /v1/shards/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "axios") {
			w.Header().Set("ETag", `"e1"`)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, shardBody)
			return
		}
		if strings.Contains(r.URL.Path, "left-pad") {
			w.Header().Set("ETag", `"e2"`)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, hotBody)
			return
		}
		http.NotFound(w, r)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = ts.URL
	})
	d, c := startDaemon(t, home)
	ctx := context.Background()

	// A public package in the local inventory drives the warm list.
	purl := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := d.DB.SetPublicness(ctx, purl, "PUBLIC"); err != nil {
		t.Fatalf("set publicness: %v", err)
	}

	res, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.WarmedKeys < 2 { // axios (project) + left-pad (HOT)
		t.Errorf("warmedKeys = %d, want >= 2: %+v", res.WarmedKeys, res)
	}
	if _, found, err := d.DB.GetShard(ctx, "npm/axios/1"); err != nil || !found {
		t.Errorf("axios shard not cached (found=%v err=%v)", found, err)
	}

	// Offline server: sync still answers, errors are reported not fatal.
	ts.Close()
	res2, err := c.Sync(ctx)
	if err != nil {
		t.Fatalf("sync offline: %v", err)
	}
	if len(res2.Errors) == 0 {
		t.Log("offline sync reported no errors (cached ETag path) — acceptable")
	}
}

func TestIPCListenerServesStatus(t *testing.T) {
	home := newTestHome(t, nil)
	_, _ = startDaemon(t, home)

	c := NewIPCClient(home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status over IPC: %v", err)
	}
	if st.Home != home {
		t.Errorf("IPC status home = %q, want %q", st.Home, home)
	}
}

func TestEnsureRunningFastPathUsesLiveDaemon(t *testing.T) {
	home := newTestHome(t, nil)
	d, _ := startDaemon(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := EnsureRunning(ctx, home)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	if c.BaseURL != d.BaseURL() {
		t.Errorf("EnsureRunning base = %q, want %q", c.BaseURL, d.BaseURL())
	}
}

func TestEnsureRunningStopsAStaleVersionBeforeSpawning(t *testing.T) {
	home := t.TempDir()
	var shutdowns atomic.Int64
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/local/v1/status":
			writeJSON(w, http.StatusOK, StatusInfo{SchemaVersion: 1, Version: "v-old", Home: home})
		case "/local/v1/shutdown":
			shutdowns.Add(1)
			writeJSON(w, http.StatusOK, map[string]bool{"stopping": true})
			go srv.Close()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if err := os.WriteFile(addrFile(home), []byte(strings.TrimPrefix(srv.URL, "http://")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	spawned := errors.New("spawned replacement")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := ensureRunning(ctx, home, "v-new", func() error { return spawned })
	if !errors.Is(err, spawned) {
		t.Fatalf("ensureRunning error = %v, want replacement spawn", err)
	}
	if shutdowns.Load() != 1 {
		t.Fatalf("stale daemon shutdowns = %d, want 1", shutdowns.Load())
	}
}

func TestStopRunningWaitsForDaemonExit(t *testing.T) {
	home := newTestHome(t, nil)
	_, c := startDaemon(t, home)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	running, err := StopRunning(ctx, home)
	if err != nil || !running {
		t.Fatalf("StopRunning running=%v err=%v", running, err)
	}
	if _, err := c.Status(ctx); err == nil {
		t.Fatal("daemon still answers after StopRunning returned")
	}
}
