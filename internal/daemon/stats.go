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
}

// StatsNow computes the dashboard numbers from localdb + CAS.
func (d *Daemon) StatsNow(ctx context.Context) (Stats, error) {
	st := Stats{SchemaVersion: 1, Mode: d.Cfg.Mode, Estimated: true, CacheBudgetMB: d.Cfg.CacheBudgetMB}

	hits, err := d.DB.ListHits(ctx, 10000)
	if err != nil {
		return st, err
	}
	st.Hits = len(hits)
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

	st.Misses = d.intStat(ctx, statMisses)
	st.EvidenceBatchesSent = d.intStat(ctx, statEvidenceSent)
	st.OriginSeeds = d.intStat(ctx, statOriginSeeds)
	st.CrossVerifications = d.intStat(ctx, statCrossVerifications)
	if v, ok, _ := d.DB.GetStat(ctx, statLastUpload); ok {
		st.LastUpload = v
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
