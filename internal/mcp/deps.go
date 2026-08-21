package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// errPersistedModeDisallowsRemote is deliberately path-free. A config read
// can fail while another process replaces config.json, and returning the
// underlying error from an MCP tool would disclose the CSX home path. More
// importantly, a failed read is not permission to keep using the mode that
// happened to be in memory when this long-running process started.
var errPersistedModeDisallowsRemote = errors.New("mcp: remote access disabled by persisted mode")

// persistedModeTransport is the final consent check immediately before an
// MCP-owned HTTP request leaves the process. The higher-level operations also
// reload config so they do not enqueue evidence or even construct remote work
// in local-only mode. Keeping the check here closes the smaller race where the
// persisted mode changes after that operation-level check but before Do.
type persistedModeTransport struct {
	home string
	base http.RoundTripper
}

func (t persistedModeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cfg, err := config.Load(t.home)
	if err != nil || cfg.Mode != config.ModeCommunity {
		return nil, errPersistedModeDisallowsRemote
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// currentConfig reloads the persisted consent state for every MCP operation.
// Any read/parse failure returns an uninitialized config, which is the
// fail-closed mode. The error is intentionally not surfaced because it can
// contain the local config path.
func currentConfig(home string) *config.Config {
	cfg, err := config.Load(home)
	if err != nil {
		return config.Default()
	}
	return cfg
}

type artifactFetchFunc func(context.Context, string) ([]byte, string, error)

func (f artifactFetchFunc) Fetch(ctx context.Context, sampleID string) ([]byte, string, error) {
	return f(ctx, sampleID)
}

// NewDeps wires the real tool implementations over one CSX home, the same
// stores the daemon uses but constructed in-process — the MCP server keeps
// working with no daemon running (goal.md §3.9). The returned close func
// releases the database handle.
func NewDeps(home string) (*Deps, func() error, error) {
	if err := config.EnsureHome(home); err != nil {
		return nil, nil, err
	}
	if _, err := config.Load(home); err != nil {
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
	// Every MCP-owned HTTP client checks the persisted mode again in its
	// transport. Operation-level checks below avoid unnecessary work and queue
	// writes; this transport is the last fail-closed boundary before the wire.
	remoteTransport := persistedModeTransport{home: home, base: http.DefaultTransport}
	syncHTTP := &http.Client{Transport: remoteTransport}
	fetchHTTP := &http.Client{Transport: remoteTransport, Timeout: 30 * time.Second}
	registryHTTP := &http.Client{Transport: remoteTransport}

	// A miss for a package this machine has never synced is not an answer
	// about the network, it is an answer about the local cache. The agent
	// is asking about a library it is about to ADD, which is exactly the
	// package the warm list has no reason to hold.
	// Artifacts resolve through the peer chain: local CAS, then peers that
	// announced the sample, then the main seeder — every remote payload
	// re-verified against its content id before it is trusted or cached
	// (goal.md §15.1). Without it a search hit named a sample the agent
	// could never open, which killed the value loop one step after it
	// worked. This only downloads; nothing is uploaded here.
	fetcher := artifactFetchFunc(func(ctx context.Context, sampleID string) ([]byte, string, error) {
		live := currentConfig(home)
		if live.Mode != config.ModeCommunity {
			return nil, "", errPersistedModeDisallowsRemote
		}
		node := &peer.Node{
			CAS: store, DB: db, Ident: ident, HTTP: fetchHTTP,
			ServerURL: live.ServerURL,
		}
		return node.Fetch(ctx, sampleID)
	})

	d := &Deps{
		// Wrapped rather than passed straight through, so recording cannot
		// be forgotten by whatever calls Search next.
		//
		// The daemon's HTTP search path recorded hits and the MCP path did
		// not, so get_local_stats, csx stats and csx ui reported 0 hits
		// forever — for the surface the product is actually used through.
		// A counter presented to the user as fact has to be one.
		Search: func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			resp := engine.Search(ctx, req)
			// One retry, and only when the miss might be about a shard we
			// never had: fetch the named packages' shards, then ask again.
			// Community mode only — in local-only, naming the package to
			// the server is the thing that mode exists to prevent.
			if resp.Miss {
				live := currentConfig(home)
				if live.Mode == config.ModeCommunity {
					syncer := &search.Syncer{DB: db, ServerURL: live.ServerURL, HTTP: syncHTTP}
					if search.FetchMissing(ctx, engine, syncer, live.Mode, req) {
						resp = engine.Search(ctx, req)
					}
				}
			}
			// Reload again: mode may have changed while a community shard fetch
			// was in flight. A miss observed after revocation must not become a
			// Wanted upload candidate.
			offerID := recordSearchOutcomeReloaded(ctx, db, ident,
				func() *config.Config { return currentConfig(home) }, req, resp)
			return resp, offerID
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
			mode := currentConfig(home).Mode
			if err != nil {
				return mode, 0, err
			}
			return mode, len(rows), nil
		},
		Mode: func() string {
			return currentConfig(home).Mode
		},
		RunObserved: func(ctx context.Context, argv []string, cwd string) (int, string, string, []string, string, error) {
			return runObserved(ctx, db, ident, currentConfig(home), registryHTTP,
				func() *config.Config { return currentConfig(home) }, argv, cwd)
		},
		ReportAdoption: func(ctx context.Context, offerID, sampleID string, applied bool, buildPass *bool) (localdb.InterventionOutcome, error) {
			return reportAdoptionReloaded(ctx, db, ident, currentConfig(home),
				func() *config.Config { return currentConfig(home) }, offerID, sampleID, applied, buildPass)
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
			return localStats(ctx, db, currentConfig(home))
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
	SampleID string `json:"sampleId"`
	Goal     string `json:"goal"`
	Status   string `json:"status"`
	// Believed is what the sample's author says a competent developer or
	// model expects, which the contract then contradicts. It is the finding,
	// and it is the reason to read this reply at all.
	Believed string `json:"believed"`
	// Symbols scopes the finding. A package's findings are not all about the
	// API that was asked for, and answering "axios.post" with a correction
	// about axios.get is answering a question nobody asked.
	Symbols        []string          `json:"symbols"`
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

	// Findings first. Everything below this is a measurement of how often
	// something ran; a finding is the sentence that says the answer the
	// caller was about to write is wrong, and a model that reads only the
	// top of the reply must still get it.
	writeExplainFindings(&b, shard, p, symbol)

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

// writeExplainFindings prints the corrections the cached shard holds for
// this package, scoped to the symbol when one was named.
//
// A sample is listed under every package version it is relevant to, so the
// same finding appears in several buckets of one shard; it is printed once.
func writeExplainFindings(b *strings.Builder, shard explainShard, p domain.PURL, symbol string) {
	seen := map[string]bool{}
	var lines []string
	for _, pkg := range shard.Packages {
		pp, err := domain.ParsePURL(pkg.PURL)
		if err != nil || !strings.EqualFold(pp.Name, p.Name) || pp.Ecosystem != p.Ecosystem {
			continue
		}
		for _, smp := range pkg.Samples {
			believed := strings.TrimSpace(smp.Believed)
			if believed == "" || seen[smp.SampleID] {
				continue
			}
			if symbol != "" && !sampleCoversSymbol(smp.Symbols, symbol) {
				continue
			}
			seen[smp.SampleID] = true
			line := "- Commonly assumed: " + believed + "\n"
			line += "  Measured otherwise by " + smp.SampleID
			if smp.Goal != "" {
				line += " — " + smp.Goal
			}
			lines = append(lines, line+"\n")
		}
	}
	if len(lines) == 0 {
		return
	}
	b.WriteString("\nFindings [the contract measured something other than what is widely assumed]:\n")
	for _, l := range lines {
		b.WriteString(l)
	}
	b.WriteString("  Read the contract with get_sample before relying on any of this.\n")
}

// sampleCoversSymbol reports whether a sample answers for the named API.
//
// The comparison is on the member, because the same API is written three
// ways across the corpus — the full module path with the member, an import
// alias with the member, and the member alone — and an exact match claims
// only the rarest of them. A sample that declares no symbol at all is about
// the package, so it answers a symbol question too.
func sampleCoversSymbol(named []string, symbol string) bool {
	if len(named) == 0 {
		return true
	}
	want := symbolMember(symbol)
	for _, n := range named {
		if strings.EqualFold(symbolMember(n), want) {
			return true
		}
	}
	return false
}

// symbolMember is the last dot- or slash-separated segment of a symbol name.
func symbolMember(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, "./"); i >= 0 {
		s = s[i+1:]
	}
	return s
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
func runObserved(ctx context.Context, db *localdb.DB, ident *identity.Identity, cfg *config.Config,
	registryHTTP *http.Client, reloadConfig func() *config.Config, argv []string, cwd string) (int, string, string, []string, string, error) {
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
		checker = &registry.Checker{Cache: evidence.PublicnessCache{DB: db}, HTTP: registryHTTP}
	}

	res, _ := evidence.Scan(ctx, cwd, checker)
	var profile scanner.CommandProfile
	if res != nil {
		profile = res.Classify(argv)
	}

	exitCode, tail, runErr := evidence.Run(ctx, argv, cwd)
	if runErr != nil {
		return -1, "", "", nil, "", runErr // command never ran; nothing recorded
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
		recordCfg := cfg
		if reloadConfig != nil {
			recordCfg = reloadConfig()
		}
		recordRes := res
		if recordCfg == nil || recordCfg.Mode != config.ModeCommunity {
			// A command can outlive the consent that existed when its scan
			// began. Preserve local inventory diagnostics while removing the
			// PUBLIC classifications that make observations uploadable.
			closed := *res
			closed.Packages = append([]scanner.ResolvedPackage(nil), res.Packages...)
			for i := range closed.Packages {
				if closed.Packages[i].Publicness == scanner.PublicnessPublic {
					closed.Packages[i].Publicness = scanner.PublicnessUnknown
				}
			}
			recordRes = &closed
		}
		rec := &evidence.Recorder{DB: db, Ident: ident, Cfg: recordCfg}
		_ = rec.RecordRun(ctx, cwd, recordRes, profile, exitCode, tail) // best-effort
	}
	// The tail goes back to the caller unredacted. Sanitizing is what the
	// UPLOAD needs; this return value never leaves the machine it was
	// produced on, and the agent reading it already has the source.
	return exitCode, string(stage), string(result), sanitized, tail, nil
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
	cfg *config.Config, offerID, sampleID string, applied bool, buildPass *bool) (localdb.InterventionOutcome, error) {
	return reportAdoptionReloaded(ctx, db, ident, cfg, nil, offerID, sampleID, applied, buildPass)
}

func reportAdoptionReloaded(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, reloadConfig func() *config.Config, offerID, sampleID string, applied bool, buildPass *bool) (localdb.InterventionOutcome, error) {

	var pass sql.NullBool
	if buildPass != nil {
		pass = sql.NullBool{Bool: *buildPass, Valid: true}
	}
	// Decide consent and construct the outbox payload before spending the
	// one-use offer token. Community correlation and enqueue then commit in
	// one SQLite transaction; local-only correlation passes an empty payload.
	uploadCfg := cfg
	if reloadConfig != nil {
		uploadCfg = reloadConfig()
	}
	outboxPayload := ""
	if uploadCfg != nil && uploadCfg.Mode == config.ModeCommunity {
		epoch := time.Now().UTC().Format("2006-01-02")
		raw, err := json.Marshal(adoptionPayload{
			SchemaVersion: 1,
			EvidenceClass: string(domain.ClassAdoptionEvidence),
			Epoch:         epoch,
			AnonID:        ident.AnonID(epoch),
			SampleID:      sampleID,
			Applied:       applied,
			BuildPass:     buildPass,
		})
		if err != nil {
			return localdb.InterventionOutcome{}, err
		}
		outboxPayload = string(raw)
	}
	return db.CorrelateInterventionAdoption(ctx, offerID, sampleID, applied, pass, outboxPayload)
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
	if funnel, err := db.InterventionSummary(ctx); err == nil {
		stats["exactFailureMatches"] = funnel.ExactFailureMatches
		stats["verifiedDetoursOffered"] = funnel.VerifiedDetoursOffered
		stats["verifiedDetoursApplied"] = funnel.Applied
		stats["postHitPass"] = funnel.PostHitPass
		stats["postHitFail"] = funnel.PostHitFail
		stats["postHitUnknown"] = funnel.PostHitUnknown
		stats["reportedFailuresAvoided"] = funnel.ReportedFailuresAvoided
	}
	return stats, nil
}

// recordSearchOutcome writes the local hit row behind csx stats, csx ui and
// get_local_stats. Queries stay on the machine — the hits table is never
// uploaded — and a failure here must never break a search, so the error is
// dropped deliberately rather than surfaced.
func recordSearchOutcome(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, req domain.SearchRequest, resp domain.SearchResponse) string {
	return recordSearchOutcomeReloaded(ctx, db, ident, func() *config.Config { return cfg }, req, resp)
}

// reloadedConfig re-reads the live config, because the mode can change
// while a shard fetch is in flight and anything observed after a
// revocation must not be uploaded.
func reloadedConfig(reload func() *config.Config) *config.Config {
	if reload == nil {
		return nil
	}
	return reload()
}

func recordSearchOutcomeReloaded(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	reloadConfig func() *config.Config, req domain.SearchRequest, resp domain.SearchResponse) string {
	if db == nil {
		return ""
	}
	if resp.Miss || len(resp.Results) == 0 {
		// A miss is a demand signal. Agents arrive over MCP, so this is the
		// path where questions actually get asked — and it was the one path
		// that threw them away.
		evidence.QueueWanted(ctx, db, ident, reloadedConfig(reloadConfig), req)
		return ""
	}
	top := resp.Results[0]
	now := time.Now().UTC()
	// Reloaded here as well as on the miss branch: the mode can change
	// while a shard fetch is in flight, and a hit observed after
	// revocation must not upload either.
	cfg := reloadedConfig(reloadConfig)
	offerID, _ := db.RecordSearchOffer(ctx, localdb.HitRow{
		TS:       now,
		Query:    req.Query,
		Grade:    top.Grade,
		SampleID: top.SampleID,
	}, localdb.InterventionRow{
		TS:                  now,
		SampleID:            top.SampleID,
		ExactFailureMatched: top.ExactFailureMatched,
		VerifiedOffer:       top.VerifiedOffer(),
	})
	// The other half of the signal. A miss has always left the machine as a
	// Wanted ask; a hit stopped at the local hits table, so the network could
	// see the demand it could not satisfy and nothing of the demand it could.
	// Counts only, community mode only — QueueSearchHit enforces both.
	evidence.QueueSearchHit(ctx, db, ident, cfg, evidence.SearchHit{
		Grade:        top.Grade,
		ResultsShown: len(resp.Results),
		OfferID:      offerID,
		SampleID:     top.SampleID,
	})
	return offerID
}
