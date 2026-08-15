package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/peer"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/cas"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// maxFileReturn caps each returned sample file (contract C8: ≤64KB/file).
const maxFileReturn = 64 * 1024

// hitListLimit bounds list_local_hits output.
const hitListLimit = 50

// NewDeps wires the real tool implementations over one CSX home, the same
// stores the daemon uses but constructed in-process — the MCP server keeps
// working with no daemon running (goal.md §3.9). The returned close func
// releases the database handle.
func NewDeps(home string) (*Deps, func() error, error) {
	if err := config.EnsureHome(home); err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load(home)
	if err != nil {
		return nil, nil, err
	}
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		return nil, nil, err
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		return nil, nil, err
	}
	store, err := cas.Open(filepath.Join(home, "cas"))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	engine := search.Engine{DB: db}
	// A miss for a package this machine has never synced is not an answer
	// about the network, it is an answer about the local cache. The agent
	// is asking about a library it is about to ADD, which is exactly the
	// package the warm list has no reason to hold.
	syncer := &search.Syncer{DB: db, ServerURL: cfg.ServerURL, HTTP: http.DefaultClient}
	// Artifacts resolve through the peer chain: local CAS, then peers that
	// announced the sample, then the main seeder — every remote payload
	// re-verified against its content id before it is trusted or cached
	// (goal.md §15.1). Without it a search hit named a sample the agent
	// could never open, which killed the value loop one step after it
	// worked. This only downloads; nothing is uploaded here.
	fetcher := &peer.Node{CAS: store, DB: db, Ident: ident, ServerURL: cfg.ServerURL}

	d := &Deps{
		// Wrapped rather than passed straight through, so recording cannot
		// be forgotten by whatever calls Search next.
		//
		// The daemon's HTTP search path recorded hits and the MCP path did
		// not, so get_local_stats, csx stats and csx ui reported 0 hits
		// forever — for the surface the product is actually used through.
		// A counter presented to the user as fact has to be one.
		Search: func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
			resp := engine.Search(ctx, req)
			// One retry, and only when the miss might be about a shard we
			// never had: fetch the named packages' shards, then ask again.
			// Community mode only — in local-only, naming the package to
			// the server is the thing that mode exists to prevent.
			if resp.Miss && search.FetchMissing(ctx, engine, syncer, cfg.Mode, req) {
				resp = engine.Search(ctx, req)
			}
			recordSearchOutcome(ctx, db, ident, cfg, req, resp)
			return resp
		},
		GetSample: func(ctx context.Context, id string) (domain.SampleManifest, map[string]string, error) {
			return getSample(ctx, db, store, fetcher, id)
		},
		Explain: func(ctx context.Context, purl, symbol string, env domain.EnvironmentFingerprint) (string, json.RawMessage, error) {
			return explainFromShards(ctx, db, purl, symbol, env)
		},
		Overview: func(ctx context.Context, purls []string, env domain.EnvironmentFingerprint) ([]PackageOverview, error) {
			return overviewFromShards(ctx, db, purls)
		},
		LocalReadiness: func(ctx context.Context) (string, int, error) {
			rows, err := db.ListShards(ctx)
			if err != nil {
				return cfg.Mode, 0, err
			}
			return cfg.Mode, len(rows), nil
		},
		Mode: func() string {
			if cfg == nil {
				return ""
			}
			return cfg.Mode
		},
		RunObserved: func(ctx context.Context, argv []string, cwd string) (int, string, string, []string, error) {
			return runObserved(ctx, db, ident, cfg, argv, cwd)
		},
		ReportAdoption: func(ctx context.Context, sampleID string, applied bool, buildPass *bool) error {
			return reportAdoption(ctx, db, ident, cfg, sampleID, applied, buildPass)
		},
		Propose: func(ctx context.Context, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
			spec, prompt, workdir, err := propose(ctx, home, goal, pkgs, symbols)
			if err == nil {
				// Remember it, or the workspace gets filled in and forgotten:
				// publishing needs the user's approval, and nobody can approve
				// what they were never told about.
				_ = db.SaveProposal(ctx, localdb.ProposalRow{
					Workdir: workdir, Goal: goal, Packages: pkgs,
				})
			}
			return spec, prompt, workdir, err
		},
		LocalHits: func(ctx context.Context) ([]localdb.HitRow, error) {
			return db.ListHits(ctx, hitListLimit)
		},
		LocalStats: func(ctx context.Context) (map[string]any, error) {
			return localStats(ctx, db, cfg)
		},
	}
	return d, db.Close, nil
}

// getSample loads sample metadata and unpacks the artifact from the CAS
// into a throwaway directory (samples.Unpack enforces the same safety rules
// as artifact creation). Files come back capped at 64KB each; binaries are
// skipped. Missing artifact with known metadata degrades to metadata-only.
// artifactFetcher resolves a sample artifact from the cheapest source.
// *peer.Node implements it.
type artifactFetcher interface {
	Fetch(ctx context.Context, sampleID string) ([]byte, string, error)
}

func getSample(ctx context.Context, db *localdb.DB, store *cas.Store,
	fetch artifactFetcher, id string) (domain.SampleManifest, map[string]string, error) {
	var manifest domain.SampleManifest
	row, haveMeta, err := db.GetSample(ctx, id)
	if err != nil {
		return manifest, nil, err
	}
	if haveMeta {
		_ = json.Unmarshal([]byte(row.ManifestJSON), &manifest)
	}

	var tgz []byte
	if rc, gerr := store.Get(id); gerr == nil {
		data, rerr := io.ReadAll(io.LimitReader(rc, samples.MaxCompressedBytes+1))
		rc.Close()
		if rerr != nil {
			return manifest, nil, rerr
		}
		tgz = data
	} else {
		// Not cached. A search hit names a sample the agent has never seen
		// before, so this is the normal path, not the exception.
		if fetch == nil {
			return manifest, nil, fmt.Errorf("sample %s is not cached and no fetcher is configured", id)
		}
		data, _, ferr := fetch.Fetch(ctx, id)
		if ferr != nil {
			if haveMeta {
				return manifest, nil, nil // metadata only; artifact unreachable
			}
			return manifest, nil, fmt.Errorf("sample %s could not be fetched: %w", id, ferr)
		}
		tgz = data
	}

	tmp, err := os.MkdirTemp("", "csx-sample-*")
	if err != nil {
		return manifest, nil, err
	}
	defer os.RemoveAll(tmp)
	if err := samples.Unpack(tgz, tmp); err != nil {
		return manifest, nil, err
	}

	files := map[string]string{}
	err = filepath.WalkDir(tmp, func(p string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, rerr := filepath.Rel(tmp, p)
		if rerr != nil {
			return rerr
		}
		content, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytesIndexZero(content) {
			return nil // skip binaries
		}
		if len(content) > maxFileReturn {
			content = append(content[:maxFileReturn], []byte("\n…[truncated at 64KB]")...)
		}
		files[filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		return manifest, nil, err
	}

	if !haveMeta {
		if raw, ok := files["csx.json"]; ok {
			_ = json.Unmarshal([]byte(raw), &manifest)
		}
	}
	_ = db.TouchSample(ctx, id) // feeds cache eviction ordering; best-effort
	return manifest, files, nil
}

func bytesIndexZero(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// Shard wire shapes (contract C6) — only the fields Explain reads.
type explainShard struct {
	GeneratedAt string            `json:"generatedAt"`
	Packages    []explainShardPkg `json:"packages"`
}

type explainShardPkg struct {
	PURL    string           `json:"purl"`
	Symbols []explainSymbol  `json:"symbols"`
	Samples []explainSampleE `json:"samples"`
}

type explainSymbol struct {
	Family   string          `json:"family"`
	Stats    explainStats    `json:"stats"`
	Failures []explainChange `json:"failures"`
}

type explainStats struct {
	ObservationCount  int64                      `json:"observationCount"`
	UniquePeerBuckets int64                      `json:"uniquePeerBuckets"`
	PassRate          float64                    `json:"passRate"`
	ByStage           map[string]explainStageQty `json:"byStage"`
	Confidence        string                     `json:"confidence"`
	LastSeen          string                     `json:"lastSeen"`
}

type explainStageQty struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
}

type explainChange struct {
	ErrorCode   string            `json:"errorCode"`
	Fingerprint string            `json:"fingerprint"`
	Count       int64             `json:"count"`
	EnvSummary  map[string]string `json:"envSummary"`
}

type explainSampleE struct {
	SampleID       string            `json:"sampleId"`
	Goal           string            `json:"goal"`
	Status         string            `json:"status"`
	ContractStages map[string]string `json:"contractStages"`
}

// explainFromShards renders the honest compatibility picture for one
// package/symbol from the locally cached shard. Observation evidence
// (PROJECT_* stages, USAGE_OBSERVATION) and verification evidence
// (contract runs, SAMPLE_VERIFICATION) are reported in separate sections
// and never summed (goal.md §3.5, docs/execution-context.md §6).
// maxOverviewPackages bounds the per-miss lookup: a dependency list can be
// hundreds long, and a miss reply is a hint, not a report.
const maxOverviewPackages = 8

// overviewFromShards summarizes each package's cached evidence. An
// unparseable purl or an uncached shard is reported as Cached=false —
// absence of data is UNKNOWN, never incompatibility (goal.md §3.6).
func overviewFromShards(ctx context.Context, db *localdb.DB, purls []string) ([]PackageOverview, error) {
	if len(purls) > maxOverviewPackages {
		purls = purls[:maxOverviewPackages]
	}
	out := make([]PackageOverview, 0, len(purls))
	for _, purlStr := range purls {
		o := PackageOverview{PURL: purlStr}
		p, err := domain.ParsePURL(purlStr)
		if err != nil {
			out = append(out, o)
			continue
		}
		row, found, err := db.GetShard(ctx, p.Ecosystem+"/"+p.Name+"/"+p.Major())
		if err != nil {
			return nil, err
		}
		if !found {
			out = append(out, o)
			continue
		}
		var shard explainShard
		if err := json.Unmarshal([]byte(row.JSON), &shard); err != nil {
			out = append(out, o) // unreadable cache reads as no cache
			continue
		}
		o.Cached = true

		var passWeighted float64
		var topFailCount int64
		// One sample is listed under every package VERSION it was verified
		// against, so counting entries counted the same sample repeatedly:
		// a package with one sample covering axios 1.11 and 1.12 reported
		// two. This is the number an agent reads on a MISS to decide
		// whether a combination is well-trodden, so overstating it is
		// exactly the wrong direction.
		seenSamples := map[string]bool{}
		for _, pkg := range shard.Packages {
			pp, perr := domain.ParsePURL(pkg.PURL)
			if perr != nil || pp.Ecosystem != p.Ecosystem || !strings.EqualFold(pp.Name, p.Name) {
				continue
			}
			for _, sym := range pkg.Symbols {
				o.Observations += sym.Stats.ObservationCount
				passWeighted += sym.Stats.PassRate * float64(sym.Stats.ObservationCount)
				// Buckets are per-symbol sets we cannot union from the
				// shard, so report the strongest single symbol rather than
				// summing and overstating independence.
				if sym.Stats.UniquePeerBuckets > o.PeerBuckets {
					o.PeerBuckets = sym.Stats.UniquePeerBuckets
				}
				for _, f := range sym.Failures {
					if f.Count > topFailCount && f.ErrorCode != "" {
						topFailCount, o.TopFailure = f.Count, f.ErrorCode
					}
				}
			}
			for _, smp := range pkg.Samples {
				if smp.SampleID != "" && !seenSamples[smp.SampleID] {
					seenSamples[smp.SampleID] = true
					o.Samples++
				}
			}
		}
		if o.Observations > 0 {
			o.PassRate = passWeighted / float64(o.Observations)
		}
		out = append(out, o)
	}
	return out, nil
}

func explainFromShards(ctx context.Context, db *localdb.DB, purlStr, symbol string, env domain.EnvironmentFingerprint) (string, json.RawMessage, error) {
	p, err := domain.ParsePURL(purlStr)
	if err != nil {
		return "", nil, fmt.Errorf("package must be a purl like pkg:npm/axios@1.12.0: %w", err)
	}
	key := p.Ecosystem + "/" + p.Name + "/" + p.Major()
	row, found, err := db.GetShard(ctx, key)
	if err != nil {
		return "", nil, err
	}
	if !found {
		text := "No local compatibility data for " + purlStr +
			" (shard " + key + " not cached). Run `csx sync` while the server is reachable, then retry. " +
			"Absence of data means UNKNOWN — it is not evidence of incompatibility."
		return text, json.RawMessage("null"), nil
	}

	var shard explainShard
	if err := json.Unmarshal([]byte(row.JSON), &shard); err != nil {
		return "", nil, fmt.Errorf("cached shard %s is unreadable: %w", key, err)
	}

	var b strings.Builder
	b.WriteString("Compatibility: " + purlStr)
	if symbol != "" {
		b.WriteString(" " + symbol)
	}
	b.WriteString("\nSource: locally cached shard " + key)
	if !row.SyncedAt.IsZero() {
		b.WriteString(" (synced " + row.SyncedAt.UTC().Format("2006-01-02") + ")")
	}
	b.WriteString("\n")
	if ctxLabel := env.Normalize().ContextLabel(); ctxLabel != "" {
		b.WriteString("Requested execution context: " + ctxLabel +
			" — evidence from other contexts does not transfer automatically.\n")
	}

	matchedSymbols := 0
	var sampleLines []string
	// A shard lists one sample under every package version it is relevant
	// to, and this loop runs once per matching purl — so every verification
	// receipt was printed twice whenever a shard carried two version
	// buckets, making one sandboxed run read as two independent ones on the
	// page that exists to keep observation and verification apart.
	seenSample := map[string]bool{}
	for _, pkg := range shard.Packages {
		pp, perr := domain.ParsePURL(pkg.PURL)
		if perr != nil || !strings.EqualFold(pp.Name, p.Name) || pp.Ecosystem != p.Ecosystem {
			continue
		}
		for _, sym := range pkg.Symbols {
			if symbol != "" && !strings.EqualFold(sym.Family, symbol) {
				continue
			}
			matchedSymbols++
			b.WriteString("\nSymbol " + sym.Family + " (" + pkg.PURL + ")\n")
			b.WriteString("Observation evidence [USAGE_OBSERVATION — project-level co-occurrence, NOT execution proof]:\n")
			fmt.Fprintf(&b, "- observations: %d across %d independent peer buckets, pass rate %.2f\n",
				sym.Stats.ObservationCount, sym.Stats.UniquePeerBuckets, sym.Stats.PassRate)
			stages := make([]string, 0, len(sym.Stats.ByStage))
			for st := range sym.Stats.ByStage {
				stages = append(stages, st)
			}
			sort.Strings(stages)
			for _, st := range stages {
				q := sym.Stats.ByStage[st]
				fmt.Fprintf(&b, "- %s: %d pass / %d fail\n", st, q.Pass, q.Fail)
			}
			if sym.Stats.Confidence != "" {
				b.WriteString("- confidence: " + sym.Stats.Confidence + "\n")
			}
			if len(sym.Failures) > 0 {
				b.WriteString("Known failures:\n")
				for _, f := range sym.Failures {
					line := "- "
					if f.ErrorCode != "" {
						line += f.ErrorCode + " "
					}
					line += fmt.Sprintf("(count %d)", f.Count)
					if len(f.EnvSummary) > 0 {
						line += " in " + envSummaryText(f.EnvSummary)
					}
					b.WriteString(line + "\n")
				}
			}
		}
		for _, smp := range pkg.Samples {
			if smp.SampleID == "" || seenSample[smp.SampleID] {
				continue
			}
			seenSample[smp.SampleID] = true
			line := "- " + smp.SampleID
			if smp.Status != "" {
				line += " status " + smp.Status
			}
			if res, ok := smp.ContractStages["contract"]; ok {
				line += ", contract " + res
			}
			if smp.Goal != "" {
				line += " — " + smp.Goal
			}
			sampleLines = append(sampleLines, line)
		}
	}
	if matchedSymbols == 0 {
		if symbol != "" {
			b.WriteString("\nNo observation evidence for symbol " + symbol + " in the cached shard — UNKNOWN, not incompatible.\n")
		} else {
			b.WriteString("\nNo per-symbol observation evidence in the cached shard.\n")
		}
	}
	if len(sampleLines) > 0 {
		b.WriteString("\nVerification evidence [SAMPLE_VERIFICATION — sandboxed contract runs, separate from observations]:\n")
		for _, l := range sampleLines {
			b.WriteString(l + "\n")
		}
	} else {
		b.WriteString("\nVerification evidence [SAMPLE_VERIFICATION]: none cached — observation counts above are NOT verification.\n")
	}
	return b.String(), json.RawMessage(row.JSON), nil
}

func envSummaryText(summary map[string]string) string {
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+summary[k])
	}
	return strings.Join(parts, " ")
}

// runObserved is the C14 evidence loop behind run_observed_command, the
// same pipeline as `csx run`: scan → classify → run → record. The wrapped
// command's exit code passes through; returned error text is sanitizer
// output only, never raw stderr.
func runObserved(ctx context.Context, db *localdb.DB, ident *identity.Identity, cfg *config.Config, argv []string, cwd string) (int, string, string, []string, error) {
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			cwd = "."
		}
	}

	// Publicness checks only run in community mode. An agent routes every
	// build through run_observed_command, so this is the highest-frequency
	// caller of all — in local-only it was announcing the whole lockfile to
	// four public registries on each one.
	var checker *registry.Checker
	if cfg != nil && config.MayContactRegistries(cfg.Mode) {
		checker = &registry.Checker{Cache: evidence.PublicnessCache{DB: db}}
	}

	res, _ := evidence.Scan(ctx, cwd, checker)
	var profile scanner.CommandProfile
	if res != nil {
		profile = res.Classify(argv)
	}

	exitCode, tail, runErr := evidence.Run(ctx, argv, cwd)
	if runErr != nil {
		return -1, "", "", nil, runErr // command never ran; nothing recorded
	}

	stage := domain.StageUsed
	if profile.Known {
		stage = profile.Stage
	}
	result := domain.ResultPass
	var sanitized []string
	if exitCode != 0 {
		result = domain.ResultFail
		var publicNames []string
		if res != nil {
			for _, p := range res.Packages {
				if p.Publicness == scanner.PublicnessPublic {
					publicNames = append(publicNames, p.PURL.Name)
				}
			}
		}
		san := sanitizer.Sanitize(tail, stage, publicNames)
		if san.Code != "" {
			sanitized = append(sanitized, "errorCode: "+san.Code)
		}
		if san.Fingerprint != "" {
			sanitized = append(sanitized, "fingerprint: "+san.Fingerprint)
		}
		if t := strings.TrimSpace(san.Template); t != "" {
			sanitized = append(sanitized, strings.Split(t, "\n")...)
		}
	}

	if res != nil && ident != nil {
		rec := &evidence.Recorder{DB: db, Ident: ident, Cfg: cfg}
		_ = rec.RecordRun(ctx, cwd, res, profile, exitCode, tail) // best-effort
	}
	return exitCode, string(stage), string(result), sanitized, nil
}

// adoptionPayload is the queued ADOPTION_EVIDENCE wire unit. It carries the
// sample id, the outcome, and rotating anonymous identity — nothing else.
type adoptionPayload struct {
	SchemaVersion int    `json:"schemaVersion"`
	EvidenceClass string `json:"evidenceClass"` // ADOPTION_EVIDENCE
	Epoch         string `json:"epoch"`
	AnonID        string `json:"anonId"`
	SampleID      string `json:"sampleId"`
	Applied       bool   `json:"applied"`
	BuildPass     *bool  `json:"buildPass,omitempty"`
}

// reportAdoption records the hit row for local post-hit success stats and
// enqueues the anonymous adoption evidence for upload (drained by the
// daemon/`csx sync`; queues locally while the server is unreachable).
func reportAdoption(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, sampleID string, applied bool, buildPass *bool) error {

	var pass sql.NullBool
	if buildPass != nil {
		pass = sql.NullBool{Bool: *buildPass, Valid: true}
	}
	// An adoption is not a search; it is what happened to one. Inserting a
	// row here counted the same search twice — once when it was answered,
	// once when the answer was used — and csx stats reported the doubled
	// number as hits.
	updated, err := db.MarkAdopted(ctx, sampleID, applied, pass)
	if err != nil {
		return err
	}
	if !updated {
		// No search on this machine led here: an agent can report an
		// adoption for a sample it obtained another way, and that is worth
		// recording, but it is one event and not two.
		if err := db.RecordHit(ctx, localdb.HitRow{
			TS: time.Now().UTC(), SampleID: sampleID,
			Adopted: applied, PostBuildPass: pass,
		}); err != nil {
			return err
		}
	}

	epoch := time.Now().UTC().Format("2006-01-02")
	payload := adoptionPayload{
		SchemaVersion: 1,
		EvidenceClass: string(domain.ClassAdoptionEvidence),
		Epoch:         epoch,
		AnonID:        ident.AnonID(epoch),
		SampleID:      sampleID,
		Applied:       applied,
		BuildPass:     buildPass,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// Community mode only. Nothing drains this queue in local-only mode, so
	// enqueueing there built a pile of reports that would never be sent
	// while the tool told the agent they were "queued for anonymous
	// upload". The local record above is the part that is real in that
	// mode, and it is kept.
	if cfg == nil || cfg.Mode != config.ModeCommunity {
		return nil
	}
	_, err = db.Enqueue(ctx, "adoption", string(raw))
	return err
}

// propose builds the sanitized clean-room spec and workspace (goal.md §9.2,
// §9.3). Only public facts enter the spec by construction; publication is a
// separate, user-approved CLI step.
func propose(ctx context.Context, home, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
	for _, ps := range pkgs {
		if _, err := domain.ParsePURL(ps); err != nil {
			return samples.SanitizedSpec{}, "", "", fmt.Errorf("packages must be purls like pkg:npm/axios@1.12.0: %w", err)
		}
	}
	spec := samples.BuildSpec(samples.ScanInputs{
		Goal:        goal,
		Kind:        "HOW",
		Packages:    pkgs,
		Symbols:     symbols,
		Environment: environment.Collect(ctx, nil),
	})
	workdir, err := samples.NewCleanRoom(home)
	if err != nil {
		return samples.SanitizedSpec{}, "", "", err
	}
	return spec, spec.PromptText(), workdir, nil
}

// localStats assembles the local dashboard numbers: persisted stat counters
// plus live counts. Everything here is local-only data.
func localStats(ctx context.Context, db *localdb.DB, cfg *config.Config) (map[string]any, error) {
	stats := map[string]any{}
	persisted, err := db.AllStats(ctx)
	if err != nil {
		return nil, err
	}
	for k, v := range persisted {
		stats[k] = v
	}
	if cfg != nil {
		mode := cfg.Mode
		if mode == config.ModeUninitialized {
			mode = "uninitialized"
		}
		stats["mode"] = mode
	}
	// A stale build makes every other number here suspect: they were
	// produced by code the user has already replaced.
	if n := staleBuildNotice(); n != "" {
		stats["staleBuild"] = n
	}
	if n, err := db.CountHits(ctx); err == nil {
		stats["hits"] = n
	}
	if rows, err := db.ListSamples(ctx); err == nil {
		stats["cachedSamples"] = len(rows)
	}
	if items, err := db.QueuePending(ctx, 1000); err == nil {
		stats["queuedUploads"] = len(items)
	}
	if pending, err := db.PendingObservations(ctx, 1000); err == nil {
		stats["pendingObservations"] = len(pending)
	}
	return stats, nil
}

// recordSearchOutcome writes the local hit row behind csx stats, csx ui and
// get_local_stats. Queries stay on the machine — the hits table is never
// uploaded — and a failure here must never break a search, so the error is
// dropped deliberately rather than surfaced.
func recordSearchOutcome(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, req domain.SearchRequest, resp domain.SearchResponse) {
	if db == nil {
		return
	}
	if resp.Miss || len(resp.Results) == 0 {
		// A miss is a demand signal. Agents arrive over MCP, so this is the
		// path where questions actually get asked — and it was the one path
		// that threw them away.
		evidence.QueueWanted(ctx, db, ident, cfg, req)
		return
	}
	top := resp.Results[0]
	_ = db.RecordHit(ctx, localdb.HitRow{
		TS:       time.Now().UTC(),
		Query:    req.Query,
		Grade:    top.Grade,
		SampleID: top.SampleID,
	})
}
