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
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
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
	// Evidence aggregates are durable, anonymous, and already consent-gated.
	// Flush a preexisting backlog soon after an MCP-started daemon appears;
	// otherwise a daemon that lives less than one maintenance interval never
	// uploads evidence at all.
	defaultUploadFirstDelay = 5 * time.Second
	// Wanted/adoption reports are tiny and already queued.  Upload them soon
	// after an MCP-started daemon appears instead of making the public Wanted
	// board wait a full maintenance interval.
	defaultQueueFirstDelay = 2 * time.Second
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
	Peer    *peer.Node              // nil unless cfg.peerListen
	Cross   *verifier.CrossVerifier // nil unless cfg.idleVerification != off
	HTTP    *http.Client            // server-bound calls; nil = 30s default
	// WantedPublic is the fail-closed package publicness boundary used before
	// a queued miss is allowed to leave the machine.
	WantedPublic func(context.Context, domain.PURL) bool

	// Ticker cadences, overridable in tests; zero means the default.
	uploadEvery, warmEvery, budgetEvery, verifyEvery time.Duration
	uploadFirstDelay, verifyFirstDelay               time.Duration

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
	// The daemon outlives every query it answers, so it keeps the parsed
	// corpus. Reading it was the whole remaining cost of a search once the
	// receipts index landed.
	d.Engine = &search.Engine{DB: db, Corpus: search.NewCorpusCache()}
	publicChecker := &registry.Checker{Cache: evidence.PublicnessCache{DB: db}}
	d.WantedPublic = func(ctx context.Context, p domain.PURL) bool {
		return publicChecker.Check(ctx, p) == scanner.PublicnessPublic
	}
	d.Syncer = &search.Syncer{DB: db, ServerURL: cfg.ServerURL, HTTP: d.httpClient()}
	d.Batcher = &evidence.Batcher{DB: db, Ident: ident, Cfg: cfg}
	// The node is always constructed: fetching (local CAS → peers → server)
	// needs no listener and benefits every peer. Only serving and announcing
	// are gated on cfg.PeerListen, in Run.
	d.Peer = &peer.Node{
		CAS: store, DB: db, Ident: ident,
		ServerURL: cfg.ServerURL, Port: cfg.PeerPort,
	}
	if cfg.Mode == config.ModeCommunity && cfg.IdleVerification != "" && cfg.IdleVerification != "off" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		d.Cross = &verifier.CrossVerifier{
			ServerURL:        cfg.ServerURL,
			Ident:            ident,
			Cap:              sandbox.Detect(ctx),
			Env:              environment.Collect(ctx, nil),
			LastActivityFile: filepath.Join(home, "logs", "last-run.log"),
			// Re-verifying a sample another peer already caches should not
			// re-download it from the seeder (goal.md §15.1).
			Source: d.Peer,
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
	// New() reads config before this process owns the single-instance lock.
	// A concurrent `csx init --local-only` can revoke community mode in that
	// small window. Reload after acquiring the lock so a just-starting daemon
	// cannot carry the old consent state into its background loops.
	fresh, err := config.Load(d.Home)
	if err != nil {
		return fmt.Errorf("daemon: reload config after lock: %w", err)
	}
	*d.Cfg = *fresh
	if d.Syncer != nil {
		d.Syncer.ServerURL = fresh.ServerURL
	}
	if d.Peer != nil {
		d.Peer.ServerURL = fresh.ServerURL
		d.Peer.Port = fresh.PeerPort
	}
	if d.Cross != nil {
		d.Cross.ServerURL = fresh.ServerURL
	}

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
	if d.communityNetworkEnabled() {
		go tickLoopAfter(ctx, orDefault(d.uploadFirstDelay, defaultUploadFirstDelay),
			orDefault(d.uploadEvery, defaultUploadEvery), func() {
				uctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				if _, err := d.uploadNow(uctx); err != nil && ctx.Err() == nil {
					log.Printf("csx daemon: evidence upload: %v", err)
				}
			})
		go tickLoopAfter(ctx, defaultQueueFirstDelay,
			orDefault(d.uploadEvery, defaultUploadEvery), func() {
				qctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				_, _ = d.drainQueue(qctx)
			})
		go tickLoop(ctx, orDefault(d.warmEvery, defaultWarmEvery), func() {
			wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			_, _ = d.warmNow(wctx)
		})
	}
	go tickLoop(ctx, orDefault(d.budgetEvery, defaultBudgetEvery), func() {
		_, _ = storage.EnforceBudget(ctx, d.DB, d.CAS, d.Cfg.CacheBudgetMB)
	})
	if d.communityNetworkEnabled() && d.Cross != nil {
		// Idle verification is explicitly opted into and only pulls work, so
		// unlike upload/warm it starts soon after launch instead of after a
		// full interval — a peer that just enabled it should not sit idle for
		// 15 minutes while jobs wait.
		go tickLoopAfter(ctx, orDefault(d.verifyFirstDelay, defaultVerifyFirstDelay),
			orDefault(d.verifyEvery, defaultVerifyEvery), func() {
				d.Cross.OnVerified = func() {
					d.incrStat(ctx, statCrossVerifications, 1)
				}
				if err := d.Cross.RunBudget(ctx, d.Cfg.IdleVerification, false); err != nil && ctx.Err() == nil {
					log.Printf("csx daemon: cross verification: %v", err)
				}
			})
	}
	if d.communityNetworkEnabled() && d.Peer != nil && d.Cfg.PeerListen {
		d.Peer.StartAnnouncing(ctx)
		go func() { _ = d.Peer.ListenAndServe(ctx) }()
	}
}

func (d *Daemon) communityNetworkEnabled() bool {
	return d != nil && d.Cfg != nil && d.Cfg.Mode == config.ModeCommunity
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
	// An automatic loop used to discard err, leaving a deterministic poison
	// batch to fail every tick while status continued to show only the last
	// historical success. Persist the attempt and bounded current error on a
	// non-cancelled context: upload failures commonly arrive with ctx already
	// expired, and that is precisely when the diagnostic must survive.
	statCtx := context.WithoutCancel(ctx)
	_ = d.DB.SetStat(statCtx, statLastUploadAttempt, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		_ = d.DB.SetStat(statCtx, statLastUploadError, boundedUploadError(err))
	} else {
		_ = d.DB.SetStat(statCtx, statLastUploadError, "")
	}
	// Upload can return accepted work together with a refusal error. Stamp
	// only the batches the server acknowledged; refused rows stay pending in
	// the batcher and must not hide genuine progress from the liveness stats.
	if n > 0 {
		_ = d.DB.SetStat(ctx, statLastUpload, time.Now().UTC().Format(time.RFC3339))
		d.incrStat(ctx, statEvidenceSent, n)
	}
	return n, err
}

const maxLastUploadErrorRunes = 512

func boundedUploadError(err error) string {
	if err == nil {
		return ""
	}
	runes := []rune(strings.TrimSpace(err.Error()))
	if len(runes) > maxLastUploadErrorRunes {
		runes = runes[:maxLastUploadErrorRunes]
	}
	return string(runes)
}

// warmNow syncs the §11.2 shard warm list. Server failures are returned
// but never fatal to the caller loops.
func (d *Daemon) warmNow(ctx context.Context) (int, error) {
	if !d.communityNetworkEnabled() {
		return 0, nil
	}
	keys := d.warmKeyList(ctx)
	if len(keys) == 0 {
		return 0, nil
	}
	return d.Syncer.SyncAll(ctx, keys)
}

// warmKeyList builds the community-mode warm list: recent public packages
// from the local inventory → HOT keys from the last /v1/stats fetch → pinned
// config entries, deduplicated in that priority order (search.WarmKeys).
// Outside community mode it returns before making any network request.
func (d *Daemon) warmKeyList(ctx context.Context) []string {
	if !d.communityNetworkEnabled() {
		return nil
	}
	var recent []domain.PURL
	// LOCAL ONLY means nothing about YOUR PROJECTS leaves, and a shard
	// request names a package: GET /v1/shards/npm/left-pad/1, one per
	// dependency, from one address. Over a session that is the whole
	// dependency tree — which is exactly what the contract screen lists
	// under what a COMMUNITY member contributes, arriving from someone who
	// was told nothing would.
	//
	// Not just local-only: UNINITIALIZED too. Before csx init no mode has
	// been chosen, so no permission has been given — and this list names
	// the caller's own packages to the server, one request each. The
	// publicness checks were gated on exactly this reasoning hours ago and
	// this list was left behind.
	if rows, err := d.DB.ListPackages(ctx); err == nil {
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].LastSeen.After(rows[j].LastSeen) })
		for _, r := range rows {
			if r.Publicness != "PUBLIC" {
				continue // private/unknown deps never drive server fetches
			}
			// Nor do excluded ones. The warm list asks the server for a
			// shard BY NAME, so leaving an excluded package in it announced
			// the exact interest the exclusion was meant to keep private.
			if d.Cfg.IsExcluded(r.PURL.String(), r.PURL.Ecosystem, r.PURL.Name) {
				continue
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
	if !d.communityNetworkEnabled() {
		return nil
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
	if !d.communityNetworkEnabled() {
		return res
	}
	// WarmedKeys is what SUCCEEDED, not what was attempted. Assigning
	// len(keys) before the sync ran meant a completely failed sync still
	// printed "warmed shard keys: 124" and exited 0, and the number is read
	// as a statement of fact about the local cache.
	keys := d.warmKeyList(ctx)
	if len(keys) > 0 {
		warmed, err := d.Syncer.SyncAll(ctx, keys)
		res.WarmedKeys = warmed
		if err != nil {
			res.Errors = append(res.Errors, err.Error())
		}
		// S3 of the activation funnel (docs/activation-funnel.md §7). Tied to
		// a warm that SUCCEEDED, for the same reason WarmedKeys is: an offline
		// sync returns cleanly, and a stamp on that would say the stage was
		// reached on a machine whose shard cache is still empty. Local-only
		// never gets here — the mode gate above returns first — so the local
		// ledger cannot contradict the mode's own promise.
		if warmed > 0 {
			_ = d.DB.StampFirst(ctx, localdb.StatFirstSyncAt, time.Now().UTC())
		}
	}
	n, err := d.uploadNow(ctx)
	res.UploadedBatches = n
	if err != nil {
		res.Errors = append(res.Errors, err.Error())
	}
	// The upload queue is separate from the observation batches and nothing
	// ever drained it, so every adoption report ever made is still sitting
	// in it. `csx sync` said "uploaded batches: 0" and exited 0 the whole
	// time.
	q, qerr := d.drainQueue(ctx)
	res.UploadedReports = q
	if qerr != nil {
		res.Errors = append(res.Errors, qerr.Error())
	}
	// An item the server keeps rejecting stops being retried, and a report
	// that quietly stops existing is exactly the failure mode this whole
	// path was built to end. Count them so something can say so.
	if n, cerr := d.DB.QueueSetAsideCount(ctx); cerr == nil {
		res.SetAsideReports = n
	}
	return res
}

// SyncResult is the POST /local/v1/sync (and csx sync) outcome.
type SyncResult struct {
	SchemaVersion   int `json:"schemaVersion"`
	WarmedKeys      int `json:"warmedKeys"`
	UploadedBatches int `json:"uploadedBatches"`
	// UploadedReports is queued items delivered (adoption reports today).
	UploadedReports int `json:"uploadedReports"`
	// SetAsideReports is queued items that stopped being retried because
	// the server rejected them in a way retrying cannot fix.
	SetAsideReports int      `json:"setAsideReports,omitempty"`
	Errors          []string `json:"errors,omitempty"`
}
