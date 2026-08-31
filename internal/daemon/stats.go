package daemon

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Meta stat keys owned by the daemon (namespaced by localdb under "stat:").
// Publish/verification flows increment originSeeds/crossVerifications; the
// dashboard reads them with a zero default.
const (
	statMisses             = "misses"
	statEvidenceSent       = "evidenceBatchesSent"
	statLastUpload         = "lastUpload"
	statLastUploadAttempt  = "lastUploadAttempt"
	statLastUploadError    = "lastUploadError"
	statOriginSeeds        = "originSeeds"
	statCrossVerifications = "crossVerifications"
)

// avgMissLLMCalls is the fixed v1 assumption behind "estimated reasoning
// avoided": one adopted hit saves ~3 LLM reasoning calls (plan P5.5). The
// number is an ESTIMATE and every surface must label it so.
const avgMissLLMCalls = 3

// Stats is the GET /local/v1/stats body — the §12.5 dashboard numbers,
// all computed from the local store. EstimatedReasoningAvoided is an
// estimate by construction; Estimated is always true so no consumer can
// present it as measured.
type Stats struct {
	SchemaVersion             int     `json:"schemaVersion"`
	Mode                      string  `json:"mode"`
	Hits                      int     `json:"hits"`
	Misses                    int     `json:"misses"`
	Adoptions                 int     `json:"adoptions"`
	PostHitBuildReports       int     `json:"postHitBuildReports"`
	PostHitBuildPassRate      float64 `json:"postHitBuildPassRate"`
	ExactFailureMatches       int     `json:"exactFailureMatches"`
	VerifiedDetoursOffered    int     `json:"verifiedDetoursOffered"`
	VerifiedDetoursApplied    int     `json:"verifiedDetoursApplied"`
	DetourPostHitPass         int     `json:"detourPostHitPass"`
	DetourPostHitFail         int     `json:"detourPostHitFail"`
	DetourPostHitUnknown      int     `json:"detourPostHitUnknown"`
	ReportedFailuresAvoided   int     `json:"reportedFailuresAvoided"`
	EvidenceBatchesSent       int     `json:"evidenceBatchesSent"`
	OriginSeeds               int     `json:"originSeeds"`
	CrossVerifications        int     `json:"crossVerifications"`
	EstimatedReasoningAvoided int     `json:"estimatedReasoningAvoided"`
	Estimated                 bool    `json:"estimated"` // always true: the reasoning-avoided figure is an estimate
	CacheBytes                int64   `json:"cacheBytes"`
	CacheBudgetMB             int     `json:"cacheBudgetMB"`
	Packages                  int     `json:"packages"`
	QueueDepth                int     `json:"queueDepth"`
	// EvidenceRefusedTerminal is evidence the server decided it will never
	// accept. It is not pending and it was not delivered, and until it had a
	// line of its own an operator could not tell a delivery problem from a
	// server decision — the queue simply sat at its cap.
	EvidenceRefusedTerminal int    `json:"evidenceRefusedTerminal,omitempty"`
	LastUpload              string `json:"lastUpload,omitempty"`
	LastUploadAttempt       string `json:"lastUploadAttempt,omitempty"`
	LastUploadError         string `json:"lastUploadError,omitempty"`
	// CountsArePartial reports that adoption and build-report counts were
	// tallied from the newest page of hits rather than the whole store,
	// which happens once there are more hits than one page holds.
	CountsArePartial bool `json:"countsArePartial,omitempty"`
	// Readiness is where THIS install got to between first run and first
	// proven value. It is a statement about one machine and never a count of
	// anything, which is why it can exist at all: the fleet version of the
	// same question has no honest answer (docs/activation-funnel.md §3).
	Readiness Readiness `json:"readiness"`
}

// Readiness is the local activation ledger rendered for a reader
// (docs/activation-funnel.md §7). Every field is an RFC3339 instant or empty,
// and empty means the stage has not been reached — §6: an unmeasured thing
// renders as a gap, never as a zero, so a consumer can tell "no first answer
// yet" from "answered at the epoch".
//
// None of it is uploaded in any mode. S1 and S2 happen before `csx init` asks
// the mode question, so transmitting them would be collecting before consent
// exists (§2.3), and the daily anonId rotation means the server could not
// assemble a funnel from them anyway.
type Readiness struct {
	FirstRunAt      string `json:"firstRunAt,omitempty"`
	InitAt          string `json:"initAt,omitempty"`
	FirstSyncAt     string `json:"firstSyncAt,omitempty"`
	MCPFirstReadyAt string `json:"mcpFirstReadyAt,omitempty"`
	MCPLastReadyAt  string `json:"mcpLastReadyAt,omitempty"`
	FirstHitAt      string `json:"firstHitAt,omitempty"`
	FirstAdoptionAt string `json:"firstAdoptionAt,omitempty"`
	// SecondsToFirstAnswer is the §5 duration, init (the consent choice) to
	// the first hit. A pointer because nil is "not both endpoints yet" and 0
	// is a real, very fast install; an int with omitempty would collapse the
	// two into the same absence. Computed here so csx stats, csx ui and
	// get_local_stats cannot each pick different endpoints.
	SecondsToFirstAnswer *int64 `json:"secondsToFirstAnswer,omitempty"`
	// Unmeasured names the stages this install reached before anything was
	// recording them, by field key ("initAt", "firstRunAt", ...).
	//
	// A separate list rather than a sentinel inside the stamp fields, because
	// every one of those is documented as an RFC3339 instant or empty and a
	// consumer parsing them must not suddenly meet a word. Empty still means
	// "not reached"; a key listed here means "reached, before this ledger
	// existed", and the two must not render the same — the panel used to tell
	// an install that had been running for days to run csx init.
	Unmeasured []string `json:"unmeasured,omitempty"`
}

// StatsNow computes the dashboard numbers from localdb + CAS.
func (d *Daemon) StatsNow(ctx context.Context) (Stats, error) {
	st := Stats{SchemaVersion: 1, Mode: d.Cfg.Mode, Estimated: true, CacheBudgetMB: d.Cfg.CacheBudgetMB}

	hits, err := d.DB.ListHits(ctx, 10000)
	if err != nil {
		return st, err
	}
	// The TOTAL, not the size of the page just read. ListHits caps at
	// 10,000, so past that the dashboard reported exactly 10,000 hits
	// forever -- a number that stops moving is read as a stalled network
	// rather than a truncated query.
	//
	// But the counters BELOW are tallied from that page, so taking the
	// total here and the adoptions from ten thousand rows put a whole-store
	// number over a partial one: with 15,000 hits and 12,000 adoptions the
	// page holds at most 10,000 of each, and the adoption rate came out
	// wrong in the flattering direction. Either both come from the store or
	// both come from the page. They come from the store.
	total, terr := d.DB.CountHits(ctx)
	if terr != nil {
		total = len(hits)
	}
	st.Hits = total
	// When the store is larger than one page, the counters below describe
	// the newest 10,000 hits and not the whole store. Say which, rather
	// than presenting a partial tally as a total: the alternative is a
	// dashboard whose adoption count silently stops growing while its hit
	// count keeps going.
	st.CountsArePartial = total > len(hits)
	passes := 0
	for _, h := range hits {
		if h.Adopted {
			st.Adoptions++
		}
		if h.PostBuildPass.Valid {
			st.PostHitBuildReports++
			if h.PostBuildPass.Bool {
				passes++
			}
		}
	}
	if st.PostHitBuildReports > 0 {
		st.PostHitBuildPassRate = float64(passes) / float64(st.PostHitBuildReports)
	}
	rework := st.PostHitBuildReports - passes
	if est := st.Adoptions*avgMissLLMCalls - rework; est > 0 {
		st.EstimatedReasoningAvoided = est
	}
	if funnel, err := d.DB.InterventionSummary(ctx); err == nil {
		st.ExactFailureMatches = funnel.ExactFailureMatches
		st.VerifiedDetoursOffered = funnel.VerifiedDetoursOffered
		st.VerifiedDetoursApplied = funnel.Applied
		st.DetourPostHitPass = funnel.PostHitPass
		st.DetourPostHitFail = funnel.PostHitFail
		st.DetourPostHitUnknown = funnel.PostHitUnknown
		st.ReportedFailuresAvoided = funnel.ReportedFailuresAvoided
	}

	st.Misses = d.intStat(ctx, statMisses)
	st.EvidenceBatchesSent = d.intStat(ctx, statEvidenceSent)
	st.OriginSeeds = d.intStat(ctx, statOriginSeeds)
	st.CrossVerifications = d.intStat(ctx, statCrossVerifications)
	if v, ok, _ := d.DB.GetStat(ctx, statLastUpload); ok {
		st.LastUpload = v
	}
	if v, ok, _ := d.DB.GetStat(ctx, statLastUploadAttempt); ok {
		st.LastUploadAttempt = v
	}
	if v, ok, _ := d.DB.GetStat(ctx, statLastUploadError); ok {
		st.LastUploadError = v
	}

	if led, err := d.DB.ActivationLedger(ctx); err == nil {
		st.Readiness = readinessFrom(led)
	}

	if size, err := d.CAS.TotalSize(); err == nil {
		st.CacheBytes = size
	}
	if pkgs, err := d.DB.ListPackages(ctx); err == nil {
		st.Packages = len(pkgs)
	}
	st.QueueDepth, _ = d.queueDepth(ctx)
	st.EvidenceRefusedTerminal, _ = d.DB.RefusedEvidenceCount(ctx)
	return st, nil
}

// readinessFrom renders the ledger. An unreached stage stays the empty
// string rather than becoming a formatted zero time, which is the difference
// between a panel printing "—" and a panel printing 1970.
func readinessFrom(led localdb.Activation) Readiness {
	r := Readiness{
		FirstRunAt:      stampString(led.FirstRunAt),
		InitAt:          stampString(led.InitAt),
		FirstSyncAt:     stampString(led.FirstSyncAt),
		MCPFirstReadyAt: stampString(led.MCPFirstReadyAt),
		MCPLastReadyAt:  stampString(led.MCPLastReadyAt),
		FirstHitAt:      stampString(led.FirstHitAt),
		FirstAdoptionAt: stampString(led.FirstAdoptionAt),
	}
	// Carried, not recomputed: the ledger is the only place that can still
	// tell "reached before this was recorded" from "never reached", because
	// both arrive here as the zero time.
	for k := range led.Unmeasured {
		r.Unmeasured = append(r.Unmeasured, k)
	}
	// One inference the ledger cannot make for itself: a recorded LAST
	// handshake is standing evidence that a first one happened, whether or not
	// anything wrote the stamp for it.
	//
	// Without this the panel contradicted itself two lines apart, on a farm
	// home that had been up for days:
	//
	//	MCP handshake        —  never  → restart your coding agent, then use a csx tool
	//	MCP last handshake   2026-08-30T14:23:27Z  (csx.db)
	//
	// Wrong advice a reader can see is wrong costs more than silence: it
	// invites them to go looking for a broken MCP path that is working, and it
	// makes every other line on the panel worth less.
	if r.MCPFirstReadyAt == "" && r.MCPLastReadyAt != "" &&
		!slices.Contains(r.Unmeasured, "mcpFirstReadyAt") {
		r.Unmeasured = append(r.Unmeasured, "mcpFirstReadyAt")
	}
	sort.Strings(r.Unmeasured)
	if d, ok := led.TimeToFirstAnswer(); ok {
		secs := int64(d / time.Second)
		r.SecondsToFirstAnswer = &secs
	}
	return r
}

func stampString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// intStat reads a counter stat; missing or malformed means 0.
func (d *Daemon) intStat(ctx context.Context, key string) int {
	v, ok, err := d.DB.GetStat(ctx, key)
	if err != nil || !ok {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

// incrStat adds n to a counter stat under statMu (read-modify-write).
func (d *Daemon) incrStat(ctx context.Context, key string, n int) {
	d.statMu.Lock()
	defer d.statMu.Unlock()
	cur := d.intStat(ctx, key)
	_ = d.DB.SetStat(ctx, key, strconv.Itoa(cur+n))
}
