package daemon

import (
	"context"
	"strconv"
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
	LastUpload                string  `json:"lastUpload,omitempty"`
	LastUploadAttempt         string  `json:"lastUploadAttempt,omitempty"`
	LastUploadError           string  `json:"lastUploadError,omitempty"`
	// CountsArePartial reports that adoption and build-report counts were
	// tallied from the newest page of hits rather than the whole store,
	// which happens once there are more hits than one page holds.
	CountsArePartial bool `json:"countsArePartial,omitempty"`
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

	if size, err := d.CAS.TotalSize(); err == nil {
		st.CacheBytes = size
	}
	if pkgs, err := d.DB.ListPackages(ctx); err == nil {
		st.Packages = len(pkgs)
	}
	st.QueueDepth, _ = d.queueDepth(ctx)
	return st, nil
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
