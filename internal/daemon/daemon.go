// Package daemon implements the csx local daemon (plan P4.1/P4.4): a
// localhost-only HTTP API plus a same-mux named-pipe/unix-socket listener,
// a single-instance lock, background maintenance tickers (evidence upload,
// shard warm, cache budget, peer announce, idle verification), and the
// §12.5 dashboard at /ui. Everything it serves is local; the only outbound
// traffic is the evidence/shard/server plumbing the user's mode allows,
// and all of it is best-effort offline-tolerant (goal.md §3.9).
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/peer"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
	"github.com/r2cuerdame/codesamplex/internal/verifier"
)

// Version is the build stamp shown in /local/v1/status and the UI.
// The CLI sets it from its own link-time version before starting a daemon.
var Version = "dev"

// Default background cadences (contract P4.1).
const (
	defaultUploadEvery = 15 * time.Minute
	defaultWarmEvery   = time.Hour
	defaultBudgetEvery = 24 * time.Hour
	defaultVerifyEvery = 15 * time.Minute
	// First cross-verification attempt after startup.
	defaultVerifyFirstDelay = 20 * time.Second
)

// ErrAlreadyRunning is returned by Run when another live daemon holds the
// single-instance lock for this home.
var ErrAlreadyRunning = errors.New("daemon: already running")

// Daemon is one local daemon instance bound to a CSX_HOME.
type Daemon struct {
	Cfg     *config.Config
	Home    string
	DB      *localdb.DB
	Ident   *identity.Identity
	CAS     *cas.Store
	Engine  *search.Engine
	Syncer  *search.Syncer
	Batcher *evidence.Batcher
	Peer    *peer.Node               // nil unless cfg.peerListen
	Cross   *verifier.CrossVerifier  // nil unless cfg.idleVerification != off
	HTTP    *http.Client             // server-bound calls; nil = 30s default

	// Ticker cadences, overridable in tests; zero means the default.
	uploadEvery, warmEvery, budgetEvery, verifyEvery time.Duration
	verifyFirstDelay                                 time.Duration

	batchMu sync.Mutex // serializes drain/upload/preview over the batcher
	statMu  sync.Mutex // serializes read-modify-write stat counters

	hotMu   sync.Mutex
	hotKeys []string // HOT shard keys from the last /v1/stats fetch

	start    time.Time
	addr     string // "127.0.0.1:port", set before ready closes
	ready    chan struct{}
	shutdown chan struct{}
	stopOnce sync.Once
}

// New wires a Daemon entirely from the persisted config in home:
// identity, SQLite store, CAS, search engine, shard syncer, evidence
// batcher, and — when the config enables them — the peer node and the
// idle cross-verifier.
func New(home string) (*Daemon, error) {
	if err := config.EnsureHome(home); err != nil {
		return nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, err
	}
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		return nil, err
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		return nil, err
	}
	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		db.Close()
		return nil, err
	}

	d := &Daemon{
		Cfg:      cfg,
		Home:     home,
		DB:       db,
		Ident:    ident,
		CAS:      store,
		ready:    make(chan struct{}),
		shutdown: make(chan struct{}),
	}
	d.Engine = &search.Engine{DB: db}
	d.Syncer = &search.Syncer{DB: db, ServerURL: cfg.ServerURL, HTTP: d.httpClient()}
	d.Batcher = &evidence.Batcher{DB: db, Ident: ident, Cfg: cfg}
	if cfg.PeerListen {
		d.Peer = &peer.Node{
			CAS: store, DB: db, Ident: ident,
			ServerURL: cfg.ServerURL, Port: cfg.PeerPort,
		}
	}
	if cfg.IdleVerification != "" && cfg.IdleVerification != "off" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		d.Cross = &verifier.CrossVerifier{
			ServerURL:        cfg.ServerURL,
			Ident:            ident,
			Cap:              sandbox.Detect(ctx),
			Env:              environment.Collect(ctx, nil),
			LastActivityFile: filepath.Join(home, "logs", "last-run.log"),
		}
		cancel()
	}
	return d, nil
}

// Close releases the daemon's local stores. Only needed when Run was
// never started (CLI direct fallback) or has already returned.
func (d *Daemon) Close() error {
	if d.DB != nil {
		return d.DB.Close()
	}
	return nil
}

// Ready is closed once both listeners accept connections.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// BaseURL is the TCP base URL ("http://127.0.0.1:port"). Valid after Ready.
func (d *Daemon) BaseURL() string { return "http://" + d.addr }

// addrFile publishes the live TCP address so CLI clients find the daemon
// even when the configured port is 0 (ephemeral, used by tests).
func addrFile(home string) string { return filepath.Join(home, "daemon.addr") }

// Run serves the local API on 127.0.0.1:{cfg.daemonPort} and on the
// platform IPC listener (Windows named pipe / unix socket), starts the
// background loops, and blocks until ctx is canceled, a shutdown is
// requested via the API, or a listener fails. Shutdown is graceful.
func (d *Daemon) Run(ctx context.Context) error {
	release, err := acquireLock(d.Home)
	if err != nil {
		return err
	}
	defer release()

	tcpLn, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(d.Cfg.DaemonPort)))
	if err != nil {
		return fmt.Errorf("daemon: listen tcp: %w", err)
	}
	auxLn, err := listenAux(d.Home)
	if err != nil {
		tcpLn.Close()
		return fmt.Errorf("daemon: listen ipc: %w", err)
	}

	d.addr = tcpLn.Addr().String()
	if err := os.WriteFile(addrFile(d.Home), []byte(d.addr+"\n"), 0o600); err != nil {
		tcpLn.Close()
		auxLn.Close()
		return err
	}
	defer os.Remove(addrFile(d.Home))

	// start must be set before any handler can run (requests may arrive
	// the moment Serve starts).
	d.start = time.Now()

	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 2)
	go func() { errCh <- srv.Serve(tcpLn) }()
	go func() { errCh <- srv.Serve(auxLn) }()

	bctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.startBackground(bctx)

	close(d.ready)

	var runErr error
	select {
	case <-ctx.Done():
	case <-d.shutdown:
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}
	shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := srv.Shutdown(shutCtx); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

// requestShutdown triggers a graceful stop exactly once (POST /local/v1/shutdown).
func (d *Daemon) requestShutdown() {
	d.stopOnce.Do(func() { close(d.shutdown) })
}

// httpClient is used for shard warm and evidence upload. The timeout is
// generous because an upload is a background chore: a first sync after
// scanning many projects sends thousands of rows, and a short deadline
// turned that into a permanent failure rather than a slow success.
func (d *Daemon) httpClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

// startBackground launches the P4.1 maintenance loops. Every iteration is
// best-effort: a down server never breaks local features (goal.md §3.9).
func (d *Daemon) startBackground(ctx context.Context) {
	go tickLoop(ctx, orDefault(d.uploadEvery, defaultUploadEvery), func() {
		uctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		_, _ = d.uploadNow(uctx) // community-mode gate lives in the batcher
	})
	go tickLoop(ctx, orDefault(d.warmEvery, defaultWarmEvery), func() {
		wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		_, _ = d.warmNow(wctx)
	})
	go tickLoop(ctx, orDefault(d.budgetEvery, defaultBudgetEvery), func() {
		_, _ = storage.EnforceBudget(ctx, d.DB, d.CAS, d.Cfg.CacheBudgetMB)
	})
	if d.Cross != nil {
		// Idle verification is explicitly opted into and only pulls work, so
		// unlike upload/warm it starts soon after launch instead of after a
		// full interval — a peer that just enabled it should not sit idle for
		// 15 minutes while jobs wait.
		go tickLoopAfter(ctx, orDefault(d.verifyFirstDelay, defaultVerifyFirstDelay),
			orDefault(d.verifyEvery, defaultVerifyEvery), func() {
				if err := d.Cross.RunBudget(ctx, d.Cfg.IdleVerification, false); err != nil && ctx.Err() == nil {
					log.Printf("csx daemon: cross verification: %v", err)
				}
			})
	}
	if d.Peer != nil {
		d.Peer.StartAnnouncing(ctx)
		go func() { _ = d.Peer.ListenAndServe(ctx) }()
	}
}

// tickLoop runs f on every tick until ctx is done. The first run waits a
// full interval: daemon startup must not fire network calls by surprise —
// POST /local/v1/sync exists for "now".
func tickLoop(ctx context.Context, every time.Duration, f func()) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}

// tickLoopAfter runs f once after first, then on every tick of every.
func tickLoopAfter(ctx context.Context, first, every time.Duration, f func()) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(first):
		f()
	}
	tickLoop(ctx, every, f)
}

func orDefault(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// uploadNow drains pending observation aggregates and posts them
// (community mode only; the batcher is a no-op otherwise). Successful
// uploads stamp lastUpload and count toward "automatic evidence sent".
func (d *Daemon) uploadNow(ctx context.Context) (int, error) {
	d.batchMu.Lock()
	defer d.batchMu.Unlock()
	n, err := d.Batcher.Upload(ctx, d.httpClient(), d.Cfg.ServerURL)
	if err == nil && n > 0 {
		_ = d.DB.SetStat(ctx, statLastUpload, time.Now().UTC().Format(time.RFC3339))
		d.incrStat(ctx, statEvidenceSent, n)
	}
	return n, err
}

// warmNow syncs the §11.2 shard warm list. Server failures are returned
// but never fatal to the caller loops.
func (d *Daemon) warmNow(ctx context.Context) (int, error) {
	keys := d.warmKeyList(ctx)
	if len(keys) == 0 {
		return 0, nil
	}
	return len(keys), d.Syncer.SyncAll(ctx, keys)
}

// warmKeyList builds the warm list: recent public packages from the local
// inventory → HOT keys from the last /v1/stats fetch → pinned config
// entries, deduplicated in that priority order (search.WarmKeys).
func (d *Daemon) warmKeyList(ctx context.Context) []string {
	var recent []domain.PURL
	if rows, err := d.DB.ListPackages(ctx); err == nil {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeen.After(rows[j].LastSeen) })
		for _, r := range rows {
			if r.Publicness != "PUBLIC" {
				continue // private/unknown deps never drive server fetches
			}
			recent = append(recent, r.PURL)
			if len(recent) == 50 {
				break
			}
		}
	}
	hot := d.fetchHot(ctx)
	var pinned []string
	for _, p := range d.Cfg.PinnedPackages {
		if k := shardKeyFor(p); k != "" {
			pinned = append(pinned, k)
		}
	}
	return search.WarmKeys(recent, nil, hot, pinned)
}

// shardKeyFor accepts either a purl ("pkg:npm/axios@1.12.0") or an
// already-formed shard key ("npm/axios/1").
func shardKeyFor(s string) string {
	if p, err := domain.ParsePURL(s); err == nil {
		return p.Ecosystem + "/" + p.Name + "/" + p.Major()
	}
	if strings.Count(s, "/") >= 2 {
		return s
	}
	return ""
}

// fetchHot fetches the network HOT shard list from GET {server}/v1/stats,
// best-effort: any failure returns the last successful fetch (possibly
// empty). Tolerates several field spellings so server evolution cannot
// break old clients.
func (d *Daemon) fetchHot(ctx context.Context) []string {
	fail := func() []string {
		d.hotMu.Lock()
		defer d.hotMu.Unlock()
		return append([]string(nil), d.hotKeys...)
	}
	url := strings.TrimSuffix(d.Cfg.ServerURL, "/") + "/v1/stats"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail()
	}
	resp, err := d.httpClient().Do(req)
	if err != nil {
		return fail()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) //nolint:errcheck
		return fail()
	}
	var body struct {
		HotShards []string `json:"hotShards"`
		Hot       []string `json:"hot"`
		Shards    struct {
			Hot []string `json:"hot"`
		} `json:"shards"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return fail()
	}
	hot := body.HotShards
	if len(hot) == 0 {
		hot = body.Hot
	}
	if len(hot) == 0 {
		hot = body.Shards.Hot
	}
	if len(hot) > 0 {
		d.hotMu.Lock()
		d.hotKeys = append([]string(nil), hot...)
		d.hotMu.Unlock()
	}
	return fail()
}

// SyncNow runs one full sync pass — shard warm plus evidence upload —
// and reports what happened. Offline failures land in Errors; local
// state is never harmed (goal.md §3.9, §25.F).
func (d *Daemon) SyncNow(ctx context.Context) SyncResult {
	res := SyncResult{SchemaVersion: 1}
	keys := d.warmKeyList(ctx)
	res.WarmedKeys = len(keys)
	if len(keys) > 0 {
		if err := d.Syncer.SyncAll(ctx, keys); err != nil {
			res.Errors = append(res.Errors, err.Error())
		}
	}
	n, err := d.uploadNow(ctx)
	res.UploadedBatches = n
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	return res
}

// SyncResult is the POST /local/v1/sync (and csx sync) outcome.
type SyncResult struct {
	SchemaVersion   int      `json:"schemaVersion"`
	WarmedKeys      int      `json:"warmedKeys"`
	UploadedBatches int      `json:"uploadedBatches"`
	Errors          []string `json:"errors,omitempty"`
}
