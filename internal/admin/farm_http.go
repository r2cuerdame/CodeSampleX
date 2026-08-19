package admin

import (
	"net/http"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// Instance is one machine the operator pays for.
//
// The cost is configured rather than fetched. Reading it from AWS would mean
// putting a credential on the server that an operator token already opens, and
// the number it would return is a fixed bundle price the operator already
// knows. A figure typed once is worth less than an API call and costs a great
// deal less to be wrong about.
type Instance struct {
	Name       string
	MonthlyUSD float64
}

// farmWindow is how far back the panel counts a worker's output. Long enough
// to survive one slow job, short enough that a worker that stopped an hour ago
// stops looking productive.
const farmWindow = time.Hour

func (h *handler) farm(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	// No store means no numbers. Rendering zeros would make "nothing measured"
	// and "nothing wrong" look identical, which is the failure this panel was
	// built to stop.
	if h.farmStats == nil {
		http.Error(w, "팜 지표를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	now := h.now().UTC()
	workers, err := h.farmStats.FarmWorkers(r.Context(), now.Add(-farmWindow), now)
	if err != nil {
		http.Error(w, "팜 워커를 불러오지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	health, err := h.farmStats.FarmHealthNow(r.Context(), now)
	if err != nil {
		http.Error(w, "팜 상태를 불러오지 못했습니다", http.StatusServiceUnavailable)
		return
	}

	views := make([]map[string]any, 0, len(workers))
	for _, worker := range workers {
		view := map[string]any{
			"label":        worker.Label,
			"computerName": worker.ComputerName,
			// A session issued but never refreshed is a worker that failed to
			// start. It reads as healthy in every list that shows only labels.
			"started":   !worker.LastRefreshAt.IsZero(),
			"drafts":    worker.Drafts,
			"published": worker.Published,
			"holding":   worker.Holding,
			"issuedAt":  worker.IssuedAt.UTC().Format(time.RFC3339),
			"expiresAt": worker.IdleExpiresAt.UTC().Format(time.RFC3339),
		}
		if !worker.LastRefreshAt.IsZero() {
			view["lastRefreshAt"] = worker.LastRefreshAt.UTC().Format(time.RFC3339)
			view["perHour"] = float64(worker.Drafts) / farmWindow.Hours()
		}
		views = append(views, view)
	}

	instances := make([]map[string]any, 0, len(h.instances))
	total := 0.0
	for _, instance := range h.instances {
		instances = append(instances, map[string]any{
			"name": instance.Name, "monthlyUsd": instance.MonthlyUSD,
		})
		total += instance.MonthlyUSD
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"workers":         views,
		"health":          farmHealthView(health),
		"instances":       instances,
		"monthlyTotalUsd": total,
	})
}

func farmHealthView(health serverstore.FarmHealth) map[string]any {
	view := map[string]any{
		"publicSamples":        health.PublicSamples,
		"duplicateCoordinates": health.DuplicateCoords,
		"staleClaims":          health.StaleClaims,
		"receiptsByOs":         health.ReceiptsByOS,
	}
	// The rate is what an operator reads; the count is what they act on.
	if health.PublicSamples > 0 {
		view["duplicateRate"] = float64(health.DuplicateCoords) / float64(health.PublicSamples)
	}
	return view
}
