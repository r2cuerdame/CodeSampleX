package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/activity"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const (
	authoringWorkLease = 24 * time.Hour
	// The released client gives a poll 15 seconds. Keep the server's own
	// ceiling below that so a caller gets a retryable answer and, critically,
	// a disconnected caller cannot leave an expansion query running for
	// minutes. The store has a slightly shorter PostgreSQL statement timeout
	// so the connection is canceled by PostgreSQL and remains reusable.
	authoringWorkPollTimeout = 12 * time.Second

	// authoringCandidateTTL is how long a completed candidate snapshot answers
	// polls before a refresh is started behind the next one.
	//
	// It was five minutes first, the builder's cadence. That sized the TTL by
	// staleness alone and forgot duty cycle: the refresh is a ~4-minute
	// whole-disk read, so with idle workers polling every 60s a five-minute
	// TTL keeps that read running about half the time -- and for that half
	// the website shares a two-core host with it. Thirty minutes bounds the
	// read to a few percent of the clock. Staleness inside it still costs a
	// worker at most one wasted claim: ClaimAuthoringWork inserts ON CONFLICT
	// DO NOTHING on the coordinate, so a candidate somebody else took since
	// the snapshot yields no row and the worker moves on; and a lease runs
	// 24 hours, so half an hour of not-yet-visible new work is nothing a
	// worker would have reached sooner anyway.
	authoringCandidateTTL = 30 * time.Minute

	// authoringCandidateRefreshBudget bounds one background refresh. The
	// production read is ~700MB from disk and measured 249s under farm load
	// (#173); the 12s poll ceiling is right for a request and is exactly
	// what guaranteed that read could never finish. A refresh answers no
	// caller, so it gets the budget the read actually needs, with a store
	// statement timeout beneath it so a wedged scan still frees its
	// connection.
	authoringCandidateRefreshBudget = 9 * time.Minute

	// authoringGapEvery is the poll on which the matrix gaps are offered
	// before WANTED. WANTED is somebody's explicit ask and ranks first on
	// every other poll; on this one, EXPANSION and DEPENDENCY candidates --
	// the blank cells of the matrix -- come first, in the order they already
	// carry. Measured 2026-09-02: the farm completed 157 WANTED coordinates
	// in 24 hours and zero of either other kind, while the snapshot held 198
	// linux expansion candidates and dependency_edge named 2,333 child
	// coordinates with no sample. At ~150 claims a day the 3,083-row WANTED
	// backlog is never exhausted, so without a turn of their own the gaps
	// are never reached. One poll in four costs WANTED a quarter of its
	// throughput and starts filling the gaps the same day.
	authoringGapEvery = 4
)

type authoringCandidateSnapshot struct {
	wanted    []serverstore.WantedRow
	expansion []serverstore.WantedRow
	takenAt   time.Time
}

type authoringCandidateCall struct {
	done     chan struct{}
	snapshot authoringCandidateSnapshot
	err      error
}

type authoringCandidateGate struct {
	mu   sync.Mutex
	call *authoringCandidateCall // the synchronous first scan, coalesced

	// The last completed answer, served to every poll inside the TTL and to
	// stale polls while one refresh runs behind them.
	have     bool
	snapshot authoringCandidateSnapshot
	takenAt  time.Time
	refresh  *authoringCandidateCall // the background refresh, coalesced
}

// loadAuthoringCandidates answers a poll from the last completed candidate
// snapshot when one exists, and reads the corpus otherwise.
//
// The read is whole-corpus and, on production, whole-disk: ~700MB against a
// 320MB buffer cache, 249s under farm load, against a 10s statement timeout
// (#173). Three workers polling every 60s ran that scan to its timeout for
// half of every minute and none of them ever received expansion work. This
// used to refuse to cache "so a later poll sees newly verified work"; the
// claim that follows a poll is authoritative (ON CONFLICT DO NOTHING on the
// coordinate), so what a stale snapshot costs is a wasted attempt, never a
// duplicate assignment, and a bounded staleness is the right trade for a
// read the host cannot afford per request.
//
// Three paths:
//
//   - a snapshot inside authoringCandidateTTL answers the poll outright;
//   - a stale snapshot answers the poll outright too, and one refresh is
//     started behind it under its own budget -- later stale polls join that
//     refresh, they do not start another;
//   - no snapshot at all keeps the original behaviour: one coalesced scan
//     bound to the poll's deadline, whose failure the poll reports. That is
//     the state a fresh process is in, and why the first poll after a deploy
//     can still be refused while every later one is not.
func (a *api) loadAuthoringCandidates(ctx context.Context, store serverstore.AuthoringSessionStore) (authoringCandidateSnapshot, error) {
	now := a.now()
	g := &a.authoringCandidates
	g.mu.Lock()
	if g.have {
		snap := g.snapshot
		if now.Sub(g.takenAt) >= authoringCandidateTTL && g.refresh == nil {
			g.refresh = a.startCandidateRefresh(ctx, store)
		}
		g.mu.Unlock()
		return snap, nil
	}
	call := g.call
	if call == nil {
		call = &authoringCandidateCall{done: make(chan struct{})}
		g.call = call
		baseCtx := context.WithoutCancel(ctx)
		var callCtx context.Context
		var cancel context.CancelFunc
		if deadline, ok := ctx.Deadline(); ok {
			// WithoutCancel intentionally ignores a disconnected first caller so
			// joined workers can still receive the result. Put the poll's absolute
			// deadline back: candidate discovery gets only the time that remains,
			// never a fresh full timeout after session refresh was slow.
			callCtx, cancel = context.WithDeadline(baseCtx, deadline)
		} else {
			callCtx, cancel = context.WithTimeout(baseCtx, a.d.authoringWorkTimeout)
		}
		callCtx = serverstore.WithAuthoringPoll(callCtx)
		go func() {
			defer cancel()
			call.snapshot, call.err = a.readCandidates(callCtx, store, store.ListAuthoringExpansionCandidates)
			g.mu.Lock()
			if call.err == nil {
				now := a.now()
				call.snapshot.takenAt = now
				g.have, g.snapshot, g.takenAt = true, call.snapshot, now
			}
			close(call.done)
			if g.call == call {
				g.call = nil
			}
			g.mu.Unlock()
		}()
	}
	g.mu.Unlock()

	select {
	case <-call.done:
		return call.snapshot, call.err
	case <-ctx.Done():
		return authoringCandidateSnapshot{}, ctx.Err()
	}
}

// startCandidateRefresh begins one background re-read of the corpus under
// the refresh budget, detached from the poll that noticed the snapshot was
// stale. The caller holds the gate lock.
func (a *api) startCandidateRefresh(ctx context.Context, store serverstore.AuthoringSessionStore) *authoringCandidateCall {
	g := &a.authoringCandidates
	call := &authoringCandidateCall{done: make(chan struct{})}
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authoringCandidateRefreshBudget)
	go func() {
		defer cancel()
		snap, err := a.readCandidates(refreshCtx, store, store.ListAuthoringExpansionCandidatesUnhurried)
		g.mu.Lock()
		if err == nil {
			now := a.now()
			snap.takenAt = now
			g.have, g.snapshot, g.takenAt = true, snap, now
		} else {
			// The old snapshot stays. The next stale poll starts another try;
			// nothing is served that was not once true.
			log.Printf("csx-server: authoring candidate refresh failed (%v); serving the previous snapshot", err)
		}
		call.snapshot, call.err = snap, err
		close(call.done)
		if g.refresh == call {
			g.refresh = nil
		}
		g.mu.Unlock()
	}()
	return call
}

// readCandidates performs the pair of reads behind one snapshot. expansion
// is the read to use for the slower half: the poll-bounded one for a first
// scan, the unhurried one for a refresh.
func (a *api) readCandidates(ctx context.Context, store serverstore.AuthoringSessionStore,
	expansion func(context.Context, int) ([]serverstore.WantedRow, error)) (authoringCandidateSnapshot, error) {
	var snap authoringCandidateSnapshot
	snap.takenAt = a.now()
	var err error
	snap.wanted, err = a.d.Store.TopWanted(ctx, 200)
	if err != nil {
		return authoringCandidateSnapshot{}, err
	}
	rows, eerr := expansion(ctx, 200)
	switch {
	case eerr == nil:
		snap.expansion = rows
	case expansionUnavailable(eerr):
		// Expansion is the network choosing its own next move on top of
		// WANTED, which is somebody's explicit ask. When the slower read
		// cannot answer in time, narrowing what a worker is offered is the
		// right loss; refusing the poll throws away the work the fast read
		// already found. A farm node measured what refusing costs: HTTP 503
		// to every poll from 2026-09-01T22:03Z, three slots idle for hours.
		log.Printf("csx-server: authoring expansion candidates unavailable (%v); "+
			"serving WANTED-only work this snapshot", eerr)
	default:
		return authoringCandidateSnapshot{}, eerr
	}
	return snap, nil
}

// gapsFirst moves every non-WANTED candidate ahead of the WANTED ones,
// keeping each group in the order it arrived. It is what a gap turn does to
// the offer; the claim that follows is unchanged, so a coordinate another
// worker took in the meantime yields no row and the loop moves on.
func gapsFirst(eligible []serverstore.WantedRow) []serverstore.WantedRow {
	out := make([]serverstore.WantedRow, 0, len(eligible))
	for _, c := range eligible {
		if c.Kind != "WANTED" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return eligible
	}
	for _, c := range eligible {
		if c.Kind == "WANTED" {
			out = append(out, c)
		}
	}
	return out
}

// authoringWorkBusyErr reports whether err is the database saying "not now"
// rather than "this is broken": a deadline, a cancellation, a statement
// timeout, or a pool with nothing free.
func authoringWorkBusyErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		serverstore.IsQueryTimeout(err) || serverstore.IsPoolBusy(err)
}

// expansionUnavailable is the narrower question: did the EXPANSION source
// alone fail, while the poll still has time to answer from WANTED?
//
// Deliberately not authoringWorkBusyErr. A blown poll deadline or a
// cancellation is about the whole request -- the caller is out of time, or
// gone -- and serving a partial answer into it helps nobody. Only a
// PostgreSQL statement timeout (this query's own 10s bound, inside the poll's
// 12s one) or a pool with nothing free means this one read could not answer
// while the request itself is still live.
//
// The distinction is not academic. Swallowing the deadline made
// TestAuthoringCandidateScanPreservesThePollAbsoluteDeadline pass or fail
// depending on WHICH of the two reads the deadline happened to land on: it
// passed here and failed on the Windows runner.
func expansionUnavailable(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	return serverstore.IsQueryTimeout(err) || serverstore.IsPoolBusy(err)
}

func writeAuthoringWorkBusy(w http.ResponseWriter, err error) bool {
	if !authoringWorkBusyErr(err) {
		return false
	}
	w.Header().Set("Retry-After", "5")
	writeErr(w, http.StatusServiceUnavailable, "authoring work is busy; retry shortly")
	return true
}

var authoringSupportedEcosystems = map[string]bool{
	"npm": true, "pypi": true, "golang": true, "cargo": true,
	"composer": true, "gem": true, "pub": true, "hex": true, "maven": true,
}

type authoringWorkRequest struct {
	SchemaVersion     int                      `json:"schemaVersion"`
	SandboxCapability domain.SandboxCapability `json:"sandboxCapability"`
	VerifierOS        []string                 `json:"verifierOS"`
	ClientVersion     string                   `json:"clientVersion"`
}

// minAuthoringClient is the first release whose worker asks the Docker daemon
// which kind of container it serves. Everything before it sent the literal
// "linux" whatever daemon it was talking to, so on a Windows daemon it
// produced receipts stamped linux — false evidence in a network whose product
// is that the environment recorded is the environment that ran.
//
// The server cannot tell an old worker on Linux from an old worker on Windows:
// both say the same thing. So the gate is on the client's ability to answer at
// all, not on the answer.
const minAuthoringClient = "v0.1.22"

// authoringClientGateStatus is 426: the request is fine, the client is not.
const authoringClientGateStatus = http.StatusUpgradeRequired

// checkAuthoringClient refuses a worker that cannot report its own platform.
// An unparseable or absent version is refused too — a worker the network
// cannot identify is exactly the one it must not trust to describe itself.
func checkAuthoringClient(version string) error {
	v := strings.TrimSpace(version)
	if v == "" || !authoringClientVersion.MatchString(v) {
		return fmt.Errorf("this worker does not report a release version; run `csx update` to %s or newer", minAuthoringClient)
	}
	if domain.CompareVersions(v, minAuthoringClient) < 0 {
		return fmt.Errorf("worker %s cannot report its container platform; run `csx update` to %s or newer", v, minAuthoringClient)
	}
	return nil
}

var authoringClientVersion = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func readAuthoringWorkRequest(w http.ResponseWriter, r *http.Request) (authoringWorkRequest, bool) {
	// v0.1.18 and older send an empty body. Their verifier adapters are all
	// pinned Linux containers, so preserve compatibility without pretending
	// the Windows host itself is the execution target.
	request := authoringWorkRequest{SchemaVersion: 1, SandboxCapability: domain.CapContainerRun, VerifierOS: []string{"linux"}}
	if r.ContentLength == 0 {
		// An empty body is a pre-v0.1.19 worker. It cannot report its
		// platform, so it is refused for the same reason as any other client
		// that cannot: the server has no way to know what it actually ran on.
		writeErr(w, authoringClientGateStatus, checkAuthoringClient("").Error())
		return authoringWorkRequest{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid authoring environment")
		return authoringWorkRequest{}, false
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid authoring environment")
		return authoringWorkRequest{}, false
	}
	if request.SchemaVersion != 1 || (request.SandboxCapability != domain.CapContainerRun && request.SandboxCapability != domain.CapCompileOnly) || len(request.VerifierOS) > 4 {
		writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
		return authoringWorkRequest{}, false
	}
	if err := checkAuthoringClient(request.ClientVersion); err != nil {
		writeErr(w, authoringClientGateStatus, err.Error())
		return authoringWorkRequest{}, false
	}
	normalized, err := normalizeVerifierOS(request.VerifierOS)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
		return authoringWorkRequest{}, false
	}
	request.VerifierOS = normalized
	if request.SandboxCapability == domain.CapContainerRun && len(normalized) == 0 {
		writeErr(w, http.StatusBadRequest, "unsupported authoring environment")
		return authoringWorkRequest{}, false
	}
	return request, true
}

// authoringVerifierOS are the container platforms this server hands work out
// for. Windows was refused outright, which is why every receipt in the network
// was stamped linux — not because nobody ran Windows, but because a worker
// that did could not say so.
var authoringVerifierOS = map[string]bool{"linux": true, "windows": true}

func normalizeVerifierOS(raw []string) ([]string, error) {
	out := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, targetOS := range raw {
		targetOS = strings.ToLower(strings.TrimSpace(targetOS))
		if !authoringVerifierOS[targetOS] {
			return nil, errors.New("unsupported verifier OS")
		}
		if seen[targetOS] {
			continue
		}
		seen[targetOS] = true
		out = append(out, targetOS)
	}
	return out, nil
}

// authoringRunnableOn reports whether an ecosystem can be built on a platform.
//
// Every supported ecosystem runs on Linux. Windows has official base images
// for only some of them, and the answer comes from sandbox rather than a list
// beside it: a list that drifts from the images hands a Windows worker a job
// that fails before its first stage.
func authoringRunnableOn(ecosystem, targetOS string) bool {
	if !authoringSupportedEcosystems[ecosystem] {
		return false
	}
	if targetOS == "windows" {
		return sandbox.SupportsWindows(ecosystem)
	}
	return true
}

func authoringCandidateEligible(candidate serverstore.WantedRow, request authoringWorkRequest) bool {
	if request.SandboxCapability != domain.CapContainerRun || candidate.Version == "" {
		return false
	}
	// A package whose NAME carries a platform and an architecture is one of
	// the per-target builds npm publishes for a native addon. It began as an
	// installability rule — @tailwindcss/oxide-darwin-arm64 cannot install on
	// Linux, and the ecosystem check waved it through because npm runs there
	// — and the measurement below made it a rule about authorability, which
	// covers the installable ones too.
	if candidate.Ecosystem == "npm" {
		if _, locked := npmPackagePlatform(candidate.Name); locked {
			// Not "wrong platform" — no platform. npm publishes one of these
			// per target for a native addon, and its main is the .node
			// binary: measured on the registry,
			// @tailwindcss/oxide-linux-x64-gnu is
			// tailwindcss-oxide.linux-x64-gnu.node and @napi-rs/lzma-linux-
			// x64-gnu is lzma.linux-x64-gnu.node, while their parents
			// @tailwindcss/oxide and @napi-rs/lzma are index.js.
			//
			// The OS check that used to be here refused a worker on the
			// wrong platform and handed a linux worker the linux one, which
			// installs perfectly and still cannot be written against: the
			// thing a sample would import is a binary the parent selects
			// internally. The parent is the package worth a sample, and it
			// does not match this pattern.
			return false
		}
	}
	// A Gradle plugin marker has no code in it, so no contract can call
	// anything and the assignment can never finish. Gradle publishes one for
	// every plugin id: a pom whose only job is to point at the artifact that
	// does the work. There is no jar, no classes, no symbols.
	//
	// It was offered anyway, and one coordinate —
	// org.jetbrains.kotlin.plugin.serialization.gradle.plugin — took an
	// authoring slot on a 24-hour lease and held it. The agent tried 22
	// times, got as far as disassembling the csx binary looking for something
	// to call, and every restart was handed the same coordinate again. Sample
	// production across the network fell from 33 an hour to nothing while
	// that ran.
	//
	// The name is the proof rather than a guess. Gradle's marker convention
	// is structural: the artifactId is the plugin id with ".gradle.plugin"
	// appended, and such an artifact is always pom-only.
	if candidate.Ecosystem == "maven" && mavenPomOnlyByName(candidate.Name) {
		return false
	}
	// A WANTED row that names an OS is an ask about that platform, and a proof
	// from another one does not answer it. A row without an OS is a question
	// about the package itself, so anyone who can run it may answer -- but it
	// still has to be a job this machine can start. Returning true for every
	// WANTED regardless of environment meant a Windows worker was handed npm
	// work that has no Windows image to run in.
	if candidate.Kind == "WANTED" {
		for _, targetOS := range request.VerifierOS {
			if !authoringRunnableOn(candidate.Ecosystem, targetOS) {
				continue
			}
			if candidate.TargetOS == "" || strings.EqualFold(candidate.TargetOS, targetOS) {
				return true
			}
		}
		return false
	}
	if candidate.Kind != "FINDING" && candidate.Kind != "EXPANSION" && candidate.Kind != "DEPENDENCY" {
		return false
	}
	// TargetOS on a FINDING or an EXPANSION says where the coordinate was
	// OBSERVED, and writing a sample is not the same act as proving one on
	// that platform. Requiring the author's OS to match the observation
	// starved the queue completely: every observation this network holds is
	// recorded on Windows and the only authoring fleet runs Linux containers,
	// so the whole 200-row candidate window came back unclaimable and the
	// workers sat idle for hours asking for work that was there.
	//
	// The sample a Linux worker writes is verified on Linux and its receipt
	// says Linux, which is honest -- the coverage disclosure already states
	// that what this network observes and what it proves are different
	// platforms. A WANTED row still pins, because that is somebody's explicit
	// ask about a platform rather than a coordinate we picked ourselves.
	for _, targetOS := range request.VerifierOS {
		if authoringRunnableOn(candidate.Ecosystem, targetOS) {
			return true
		}
	}
	return false
}

// authoringNewestVersions is how many releases of one package a worker is
// steered towards. It matches the version axis the site renders, so the work
// fills cells a reader can actually see.
const authoringNewestVersions = 6

// preferNewestVersions lifts the candidates for a package's newest releases
// ahead of its older ones, leaving every row in place otherwise.
//
// It is a preference and never a filter. A candidate outside the window is
// still the only work left when nothing better is claimable, and dropping it
// would hand the worker NO_WORK instead of something to do.
//
// It exists in Go because this is where version precedence can be judged
// correctly. The store caps the sibling branch by ordering versions as
// strings, which is all SQL can express and which ranks 7.0.3 above 14.0.1 —
// the same mistake the site's own version list was fixed for. The cap is a
// safety bound and an imperfect six is acceptable there; the order work is
// handed out in is not, so it is corrected here.
func preferNewestVersions(candidates []serverstore.WantedRow, keep int) []serverstore.WantedRow {
	if keep < 1 || len(candidates) < 2 {
		return candidates
	}
	byName := map[[2]string][]string{}
	for _, c := range candidates {
		name := [2]string{c.Ecosystem, c.Name}
		byName[name] = append(byName[name], c.Version)
	}
	preferred := map[[3]string]bool{}
	for name, versions := range byName {
		sort.SliceStable(versions, func(i, j int) bool {
			return domain.CompareVersions(versions[i], versions[j]) > 0
		})
		for i, v := range versions {
			if i >= keep {
				break
			}
			preferred[[3]string{name[0], name[1], v}] = true
		}
	}
	out := make([]serverstore.WantedRow, len(candidates))
	copy(out, candidates)
	sort.SliceStable(out, func(i, j int) bool {
		ai := preferred[[3]string{out[i].Ecosystem, out[i].Name, out[i].Version}]
		aj := preferred[[3]string{out[j].Ecosystem, out[j].Name, out[j].Version}]
		return ai && !aj
	})
	return out
}

func (a *api) handleAuthoringWorkNext(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "authoring work storage unavailable")
		return
	}
	request, ok := readAuthoringWorkRequest(w, r)
	if !ok {
		return
	}
	tokenHash, ok := authoringDraftTokenHash(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	pollCtx, cancel := context.WithTimeout(r.Context(), a.d.authoringWorkTimeout)
	defer cancel()
	now := a.now().UTC()
	ip := ""
	if addr, ok := activity.ExternalRequestAddress(r); ok {
		ip = addr.String()
	}
	session, err := store.RefreshAuthoringSession(pollCtx, tokenHash, ip, "", now, now.Add(time.Hour))
	if err != nil {
		if writeAuthoringWorkBusy(w, err) {
			return
		}
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	snapshot, err := a.loadAuthoringCandidates(pollCtx, store)
	if err != nil {
		if writeAuthoringWorkBusy(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "listing authoring work failed")
		return
	}
	var funnel authoringFunnel
	funnel.Wanted = len(snapshot.wanted)
	eligible := make([]serverstore.WantedRow, 0, 400)
	for _, candidate := range snapshot.wanted {
		candidate.Kind = "WANTED"
		candidate.Score = candidate.Asks
		if authoringCandidateEligible(candidate, request) {
			eligible = append(eligible, candidate)
		}
	}
	funnel.WantedEligible = len(eligible)
	// WANTED keeps its own order: it is somebody's explicit ask, and demand is
	// the ranking. Expansion is the network choosing its own next move, so it
	// is steered at the releases the site renders.
	fresh := make([]serverstore.WantedRow, 0, len(snapshot.expansion))
	for _, candidate := range snapshot.expansion {
		if authoringCandidateEligible(candidate, request) {
			fresh = append(fresh, candidate)
		}
	}
	funnel.Expansion = len(snapshot.expansion)
	funnel.ExpansionEligible = len(fresh)
	eligible = append(eligible, preferNewestVersions(fresh, authoringNewestVersions)...)
	// A dependency coordinate is the one kind of work whose release no
	// publicness gate has necessarily seen: it exists because a lockfile
	// resolved onto it, not because anybody reported using it. Confirm it
	// against the registry before a worker is sent, and register it while we
	// are there.
	eligible = a.confirmDependencyWork(pollCtx, eligible)
	funnel.AfterDependency = len(eligible)
	// A maven coordinate that publishes only a pom — a BOM, a parent — has no
	// classes and therefore no symbol a contract could call. Asked here, once
	// per coordinate for the life of the process, because the answer is a
	// fact about the artifact and not about this worker.
	eligible = dropUnauthorableMaven(pollCtx, a.mavenJar, eligible)
	funnel.AfterUnauthorable = len(eligible)
	if a.authoringPolls.Add(1)%authoringGapEvery == 0 {
		eligible = gapsFirst(eligible)
	}
	funnel.Offered = len(eligible)
	work, found, err := store.ClaimAuthoringWork(pollCtx, session.SessionID, eligible, now, now.Add(authoringWorkLease))
	if err != nil {
		if writeAuthoringWorkBusy(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "claiming authoring work failed")
		return
	}
	snapAge := "0s"
	if !snapshot.takenAt.IsZero() {
		snapAge = now.Sub(snapshot.takenAt).Round(time.Second).String()
	}
	if !found {
		log.Printf("csx-server: authoring poll session=%s wanted=%d/%d expansion=%d/%d served=NO_WORK snapshotAge=%s",
			session.SessionID, funnel.Wanted, funnel.WantedEligible, funnel.Expansion, funnel.ExpansionEligible, snapAge)
		writeJSON(w, http.StatusOK, funnel.noWork())
		return
	}
	log.Printf("csx-server: authoring poll session=%s wanted=%d/%d expansion=%d/%d served=%s snapshotAge=%s",
		session.SessionID, funnel.Wanted, funnel.WantedEligible, funnel.Expansion, funnel.ExpansionEligible, work.Kind, snapAge)
	purl := domain.PURL{Ecosystem: work.Ecosystem, Name: work.Name, Version: work.Version}.String()
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ASSIGNED",
		"work": map[string]any{
			"ecosystem": work.Ecosystem, "name": work.Name, "version": work.Version,
			"symbol": work.Symbol, "asks": work.Asks, "kind": work.Kind, "score": work.Score, "package": purl,
			"leaseExpiresAt": work.LeaseExpiresAt.UTC(),
		},
	})
}

// authoringOutcomeRequest is a writer classifying the work it holds.
//
// The classification has to come from the worker because nothing else knows
// it. The server can observe that it gave a coordinate out and got nothing
// back; it cannot tell a Docker daemon that died from a registry that was down
// from an artifact that contains no code — and treating those three as one is
// how a bad afternoon at a registry becomes a permanent exclusion.
type authoringOutcomeRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Outcome       string `json:"outcome"`
	Detail        string `json:"detail"`
}

// handleAuthoringWorkOutcome takes a writer's classification and hands its
// claim back.
//
// Deliberately not behind the minAuthoringClient gate that /work/next uses.
// That gate exists because an old worker cannot say which container platform
// it ran on and would stamp a receipt with a platform it never used. An
// outcome report makes no claim about a platform — it says the work produced
// nothing and why — so gating it would only silence honest reports from older
// workers, which is the exact silence this endpoint exists to end.
func (a *api) handleAuthoringWorkOutcome(w http.ResponseWriter, r *http.Request) {
	store, ok := a.d.Store.(serverstore.AuthoringSessionStore)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "authoring work storage unavailable")
		return
	}
	tokenHash, ok := authoringDraftTokenHash(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	var request authoringOutcomeRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&request); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid authoring outcome")
		return
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "invalid authoring outcome")
		return
	}
	outcome := serverstore.AuthoringOutcome(strings.TrimSpace(request.Outcome))
	// AUTHORED and HANDED_OUT are the server's own bookkeeping. A client that
	// could report them could mark a coordinate solved without writing
	// anything, which would clear every counter that withholds hopeless work.
	if request.SchemaVersion != 1 || !serverstore.ValidAuthoringOutcome(outcome) {
		writeErr(w, http.StatusBadRequest, "unsupported authoring outcome")
		return
	}
	now := a.now().UTC()
	ip := ""
	if addr, ok := activity.ExternalRequestAddress(r); ok {
		ip = addr.String()
	}
	session, err := store.RefreshAuthoringSession(r.Context(), tokenHash, ip, "", now, now.Add(time.Hour))
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "authoring session unavailable")
		return
	}
	work, released, err := store.ReportAuthoringOutcome(r.Context(), session.SessionID, outcome, request.Detail, now)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "recording authoring outcome failed")
		return
	}
	if !released {
		// A report can only speak for work the writer actually holds, and a
		// writer whose lease already lapsed has done nothing wrong.
		writeJSON(w, http.StatusOK, map[string]string{"status": "NO_CLAIM"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "RELEASED",
		"work": map[string]any{
			"package": domain.PURL{Ecosystem: work.Ecosystem, Name: work.Name, Version: work.Version}.String(),
			"symbol":  work.Symbol,
		},
	})
}
