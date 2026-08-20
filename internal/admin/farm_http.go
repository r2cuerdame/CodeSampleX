package admin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/sandbox"
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

	coverage, err := h.farmStats.FarmCoverage(r.Context())
	if err != nil {
		http.Error(w, "커버리지를 불러오지 못했습니다", http.StatusServiceUnavailable)
		return
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"workers":         views,
		"health":          farmHealthView(health),
		"coverage":        farmCoverageView(coverage),
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
		"quarantinedByReason":  quarantineReasonView(health.QuarantinedByReason),
	}
	// The rate is what an operator reads; the count is what they act on.
	if health.PublicSamples > 0 {
		view["duplicateRate"] = float64(health.DuplicateCoords) / float64(health.PublicSamples)
	}
	return view
}

// farmCoverageView reports coverage per (platform, ecosystem).
//
// buildable is the field that matters. npm on Windows is thousands of
// packages observed and zero proven, and it will stay zero forever because no
// Windows Node image exists -- rendered as a progress bar it would read as a
// backlog somebody is behind on. An unbuildable cell omits proven entirely
// rather than reporting a zero, which is this file's existing idiom for
// "not measurable" versus "measured as none".
func farmCoverageView(cells []serverstore.FarmAxisCoverage) []map[string]any {
	out := make([]map[string]any, 0, len(cells))
	for i, c := range cells {
		if i >= maxFarmCoverageRows {
			break
		}
		row := map[string]any{
			"os":        clampAdminLabel(c.OS),
			"ecosystem": clampAdminLabel(c.Ecosystem),
			"observed":  c.Observed,
			"buildable": farmBuildable(c.OS, c.Ecosystem),
		}
		if row["buildable"].(bool) {
			row["measured"] = c.Measured
			row["proven"] = c.Proven
			row["observedProven"] = c.ObservedProven
		}
		out = append(out, row)
	}
	return out
}

// farmBuildable answers whether a verifier could ever run this ecosystem on
// this platform. macOS cannot be containerised at all, and on Windows only
// golang and pypi publish a base image.
func farmBuildable(os, ecosystem string) bool {
	switch strings.ToLower(strings.TrimSpace(os)) {
	case "linux":
		return true
	case "windows":
		return sandbox.SupportsWindows(ecosystem)
	}
	return false
}

// clampAdminLabel bounds a value that came from recorded evidence rather than
// from a fixed vocabulary. validEnv imposes no length limit on either field.
func clampAdminLabel(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxAdminLabelBytes {
		return v[:maxAdminLabelBytes]
	}
	return v
}

const (
	maxFarmCoverageRows = 64
	maxAdminLabelBytes  = 48
)

// quarantineReasonView orders withdrawals by how many share a reason, and
// clamps the text: the reason is operator-written prose, not a vocabulary.
//
// The unexplained bucket is labelled rather than dropped. Something was
// pulled and nobody wrote down why, which is the row an operator most needs
// to see; leaving it blank would make it look like a rendering gap.
func quarantineReasonView(byReason map[string]int) []map[string]any {
	type row struct {
		reason string
		n      int
	}
	rows := make([]row, 0, len(byReason))
	for reason, n := range byReason {
		rows = append(rows, row{reason, n})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].n != rows[j].n {
			return rows[i].n > rows[j].n
		}
		return rows[i].reason < rows[j].reason
	})
	out := make([]map[string]any, 0, len(rows))
	for i, r := range rows {
		if i >= maxQuarantineReasons {
			break
		}
		reason := strings.TrimSpace(r.reason)
		if len(reason) > maxQuarantineReasonBytes {
			reason = reason[:maxQuarantineReasonBytes] + "…"
		}
		out = append(out, map[string]any{
			"reason":      reason,
			"count":       r.n,
			"unexplained": reason == "",
		})
	}
	return out
}

const (
	maxQuarantineReasons     = 12
	maxQuarantineReasonBytes = 160
)
