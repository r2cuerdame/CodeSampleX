package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func (h *handler) reportStore() serverstore.ReportQueueStore {
	store, _ := h.csxIssues.(serverstore.ReportQueueStore)
	return store
}

func (h *handler) reports(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	store := h.reportStore()
	if store == nil {
		http.Error(w, "신고 대기열을 사용할 수 없습니다", 503)
		return
	}
	q := r.URL.Query()
	filter := serverstore.ReportFilter{Channel: q.Get("channel"), State: q.Get("state"), Query: strings.TrimSpace(q.Get("q")), Limit: 25}
	if filter.Channel == "" {
		filter.Channel = "product"
	}
	if filter.State == "" {
		filter.State = "open"
	}
	if !oneOf(filter.Channel, "all", "product", "anomaly") || !oneOf(filter.State, "all", "open", "resolved", "blocked", "unlinked") || len(filter.Query) > 200 {
		http.Error(w, "필터 값을 확인하세요", 400)
		return
	}
	if raw := q.Get("offset"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 || value > 1000000 {
			http.Error(w, "페이지 값을 확인하세요", 400)
			return
		}
		filter.Offset = value
	}
	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()
	page, err := store.ReportQueue(ctx, filter)
	if err != nil {
		http.Error(w, "신고 대기열을 불러오지 못했습니다", 503)
		return
	}
	writeAdminJSON(w, 200, page)
}

func oneOf(value string, values ...string) bool {
	for _, v := range values {
		if value == v {
			return true
		}
	}
	return false
}

func (h *handler) reportDetail(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		http.Error(w, "신고 번호를 확인하세요", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()
	if r.PathValue("channel") == "product" && h.csxIssues != nil {
		row, ok, err := h.csxIssues.CSXIssueReportByID(ctx, id)
		if err != nil {
			http.Error(w, "신고를 불러오지 못했습니다", 503)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		note := ""
		if store := h.reportStore(); store != nil {
			note, err = store.ProductReviewNote(ctx, id)
		}
		if err != nil {
			http.Error(w, "검토 기록을 불러오지 못했습니다", 503)
			return
		}
		writeAdminJSON(w, 200, map[string]any{"id": id, "channel": "product", "status": row.Status, "verdict": row.Verdict, "reason": row.ReplayReason, "canonicalRef": row.CanonicalRef, "reviewNote": note, "firstSeen": row.FirstSeen, "lastSeen": row.LastSeen, "verdictAt": row.VerdictAt, "occurrences": row.Occurrences, "evidence": serverstore.ReportEvidence(row.ReportJSON)})
		return
	}
	if r.PathValue("channel") == "anomaly" && h.anomalies != nil {
		row, ok, err := h.anomalies.AnomalyReportByID(ctx, id)
		if err != nil {
			http.Error(w, "신고를 불러오지 못했습니다", 503)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeAdminJSON(w, 200, map[string]any{"id": id, "channel": "anomaly", "status": row.Status, "verdict": row.Verdict, "reason": row.UnsupportedReason, "sampleId": row.SampleID, "jobId": row.JobID, "firstSeen": row.FirstSeen, "lastSeen": row.LastSeen, "verdictAt": row.VerdictAt, "occurrences": row.Reports, "evidence": serverstore.ReportEvidence(row.ReportJSON)})
		return
	}
	http.NotFound(w, r)
}

func (h *handler) reviewReport(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !byToken && !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", 403)
		return
	}
	store := h.reportStore()
	if store == nil {
		http.Error(w, "검토 기록을 사용할 수 없습니다", 503)
		return
	}
	var input struct {
		ID           int64  `json:"id"`
		Verdict      string `json:"verdict"`
		Note         string `json:"note"`
		CanonicalRef string `json:"canonicalRef"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(w, "검토 내용을 확인하세요", 400)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		http.Error(w, "검토 내용을 확인하세요", 400)
		return
	}
	input.Note, input.CanonicalRef = strings.TrimSpace(input.Note), strings.TrimSpace(input.CanonicalRef)
	if input.ID < 1 || !domain.ValidCSXIssueVerdict(input.Verdict) || len(input.Note) < 10 || len(input.Note) > 4000 || len(input.CanonicalRef) > 128 || (input.CanonicalRef != "" && input.Verdict != domain.CSXIssueVerdictDefect) {
		http.Error(w, "판정과 근거(10~4000바이트)를 확인하세요. 버그 연결은 확인된 결함만 가능합니다", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), dashboardTimeout)
	defer cancel()
	if _, found, err := h.csxIssues.CSXIssueReportByID(ctx, input.ID); err != nil {
		http.Error(w, "신고를 불러오지 못했습니다", 503)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}
	applied, err := store.ReviewProductReport(ctx, input.ID, input.Verdict, input.Note, input.CanonicalRef, h.now().UTC())
	if err != nil {
		http.Error(w, "검토를 저장하지 못했습니다", 503)
		return
	}
	if !applied {
		http.Error(w, "이미 판정된 신고입니다. 새로고침하여 기록을 확인하세요", 409)
		return
	}
	writeAdminJSON(w, 200, map[string]any{"applied": true})
}
