package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
)

type authoringCandidateSnapshot struct {
	wanted    []serverstore.WantedRow
	expansion []serverstore.WantedRow
}

type authoringCandidateCall struct {
	done     chan struct{}
	snapshot authoringCandidateSnapshot
	err      error
}

type authoringCandidateGate struct {
	mu   sync.Mutex
	call *authoringCandidateCall
}

// loadAuthoringCandidates collapses simultaneous fleet polls onto one pair
// of whole-corpus reads. It deliberately does not cache a completed answer:
// a later poll must see newly verified work. The query owns a bounded context
// independent of the first HTTP client, so one disconnect neither abandons
// waiters nor leaves an unbounded database operation behind.
func (a *api) loadAuthoringCandidates(ctx context.Context, store serverstore.AuthoringSessionStore) (authoringCandidateSnapshot, error) {
	a.authoringCandidates.mu.Lock()
	call := a.authoringCandidates.call
	if call == nil {
		call = &authoringCandidateCall{done: make(chan struct{})}
		a.authoringCandidates.call = call
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
			call.snapshot.wanted, call.err = a.d.Store.TopWanted(callCtx, 200)
			if call.err == nil {
				call.snapshot.expansion, call.err = store.ListAuthoringExpansionCandidates(callCtx, 200)
			}
			a.authoringCandidates.mu.Lock()
			close(call.done)
			if a.authoringCandidates.call == call {
				a.authoringCandidates.call = nil
			}
			a.authoringCandidates.mu.Unlock()
		}()
	}
	a.authoringCandidates.mu.Unlock()

	select {
	case <-call.done:
		return call.snapshot, call.err
	case <-ctx.Done():
		return authoringCandidateSnapshot{}, ctx.Err()
	}
}

func writeAuthoringWorkBusy(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) &&
		!serverstore.IsQueryTimeout(err) && !serverstore.IsPoolBusy(err) {
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
	eligible := make([]serverstore.WantedRow, 0, 400)
	for _, candidate := range snapshot.wanted {
		candidate.Kind = "WANTED"
		candidate.Score = candidate.Asks
		if authoringCandidateEligible(candidate, request) {
			eligible = append(eligible, candidate)
		}
	}
	// WANTED keeps its own order: it is somebody's explicit ask, and demand is
	// the ranking. Expansion is the network choosing its own next move, so it
	// is steered at the releases the site renders.
	fresh := make([]serverstore.WantedRow, 0, len(snapshot.expansion))
	for _, candidate := range snapshot.expansion {
		if authoringCandidateEligible(candidate, request) {
			fresh = append(fresh, candidate)
		}
	}
	eligible = append(eligible, preferNewestVersions(fresh, authoringNewestVersions)...)
	// A dependency coordinate is the one kind of work whose release no
	// publicness gate has necessarily seen: it exists because a lockfile
	// resolved onto it, not because anybody reported using it. Confirm it
	// against the registry before a worker is sent, and register it while we
	// are there.
	eligible = a.confirmDependencyWork(pollCtx, eligible)
	// A maven coordinate that publishes only a pom — a BOM, a parent — has no
	// classes and therefore no symbol a contract could call. Asked here, once
	// per coordinate for the life of the process, because the answer is a
	// fact about the artifact and not about this worker.
	eligible = dropUnauthorableMaven(pollCtx, a.mavenJar, eligible)
	work, found, err := store.ClaimAuthoringWork(pollCtx, session.SessionID, eligible, now, now.Add(authoringWorkLease))
	if err != nil {
		if writeAuthoringWorkBusy(w, err) {
			return
		}
		writeErr(w, http.StatusInternalServerError, "claiming authoring work failed")
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]string{"status": "NO_WORK"})
		return
	}
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
