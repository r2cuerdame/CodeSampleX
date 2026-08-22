package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The withheld-work panel: which public coordinates the authoring queue has
// stopped offering, why, on what evidence, for how long, and the way back.
//
// Work leaving the board silently is the failure this exists to prevent. A
// coordinate can be withheld because two independent writers measured that it
// contains no callable symbol, or because it kept being handed out and kept
// producing nothing. The first is a statement about the artifact and needs an
// operator to lift; the second is an inference about attempts and lapses on
// its own. Reading the panel has to make that difference obvious, because
// acting on them is different work.

// maxWithheldRows bounds the page. An operator scanning a list acts on the
// newest withholdings; the rest are a database query, not a dashboard.
const maxWithheldRows = 100

func (h *handler) withheldWork(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	if h.authoring == nil || h.authoring.store == nil {
		// No store means no answer. Rendering an empty list would make
		// "nothing withheld" and "cannot tell" look identical, which is the
		// confusion this panel exists to end.
		http.Error(w, "보류된 작업 정보를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	now := h.now().UTC()
	rows, err := h.authoring.store.ListAuthoringQuarantine(r.Context(), now, maxWithheldRows)
	if err != nil {
		http.Error(w, "보류된 작업을 불러오지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"withheld": withheldView(rows, now)})
}

func withheldView(rows []serverstore.AuthoringAttemptState, now time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		view := map[string]any{
			"package": domain.PURL{Ecosystem: row.Ecosystem, Name: row.Name, Version: row.Version}.String(),
			// The coordinate in parts as well as assembled: the reopen call
			// takes the parts, and re-deriving them by splitting a purl in the
			// browser is how a name containing a slash gets reopened wrong.
			"ecosystem": row.Ecosystem,
			"name":      row.Name,
			"version":   row.Version,
			"symbol":    clampAdminLabel(row.Symbol),
			"kind":      clampAdminLabel(row.Kind),
			"reason":    row.QuarantineReason,
			// The evidence, not just the verdict.
			"attempts":                    row.Attempts,
			"noOutput":                    row.NoOutput,
			"excused":                     row.Excused,
			"authored":                    row.Authored,
			"sessionsMeasuringImpossible": row.SessionsMeasuringImpossible,
			"firstAttemptAt":              row.FirstAttemptAt.UTC().Format(time.RFC3339),
			"lastAttemptAt":               row.LastAttemptAt.UTC().Format(time.RFC3339),
			"quarantinedAt":               row.QuarantinedAt.UTC().Format(time.RFC3339),
			"ageHours":                    nonNegative(now.Sub(row.QuarantinedAt)).Hours(),
			// needsOperator is the difference between the two kinds of
			// withholding: one heals by itself, the other never does.
			"needsOperator": row.ReopensAt.IsZero(),
			"history":       withheldHistoryView(row.History),
		}
		if !row.ReopensAt.IsZero() {
			view["reopensAt"] = row.ReopensAt.UTC().Format(time.RFC3339)
		}
		out = append(out, view)
	}
	return out
}

func withheldHistoryView(history []serverstore.AuthoringAttempt) []map[string]any {
	out := make([]map[string]any, 0, len(history))
	for _, entry := range history {
		out = append(out, map[string]any{
			"at":      entry.At.UTC().Format(time.RFC3339),
			"kind":    clampAdminLabel(entry.Kind),
			"outcome": string(entry.Outcome),
			// The writer's own note. It is client prose and is clamped for the
			// same reason every other recorded string on this page is.
			"detail": clampWithheldDetail(entry.Detail),
			// The session is who, and an operator chasing "one worker is
			// reporting this on everything" needs it.
			"session": clampAdminLabel(entry.SessionID),
		})
	}
	return out
}

const maxWithheldDetailBytes = 160

func clampWithheldDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) > maxWithheldDetailBytes {
		return detail[:maxWithheldDetailBytes] + "…"
	}
	return detail
}

type reopenWithheldRequest struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Symbol    string `json:"symbol"`
}

// reopenWithheldWork puts one coordinate back on the board. It is the "safe
// re-open path": the counters that took the work off reset, the history that
// records what happened does not, and a coordinate that genuinely cannot be
// authored simply earns its withholding again.
func (h *handler) reopenWithheldWork(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !byToken && !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	if h.authoring == nil || h.authoring.store == nil {
		http.Error(w, "보류된 작업 정보를 사용할 수 없습니다", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, authoringRequestLimit)
	var input reopenWithheldRequest
	if err := decodeAdminJSON(r, &input); err != nil {
		http.Error(w, "좌표를 확인하세요", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.Ecosystem) == "" || strings.TrimSpace(input.Name) == "" ||
		strings.TrimSpace(input.Version) == "" {
		http.Error(w, "좌표를 확인하세요", http.StatusBadRequest)
		return
	}
	reopened, err := h.authoring.store.ReopenAuthoringQuarantine(r.Context(),
		input.Ecosystem, input.Name, input.Version, input.Symbol, h.now().UTC())
	if err != nil {
		http.Error(w, "보류를 해제하지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	// Nothing withheld is not a failure: an operator clicking twice must not
	// see an error for work that is already back.
	writeAdminJSON(w, http.StatusOK, map[string]any{"reopened": reopened})
}
