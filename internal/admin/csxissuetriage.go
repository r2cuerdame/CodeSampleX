package admin

import (
	"net/http"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The product-defect channel could take a report and never let go of one.
//
// A report arrives, the server marks it "no replay lane: a person triages it,
// which is why it is not left pending", and then no route existed by which a
// person could record that they had. The store has held SetCSXIssueVerdict and
// LinkCSXIssueCanonical since the channel shipped; nothing exposed them, so
// the panel could show an operator eighteen open reports and no way to answer
// any of them.
//
// Measured on production 2026-08-30: 18 reports, every verdict empty, the
// oldest four days old. A channel that only accumulates teaches the agents
// reporting into it that reporting does nothing.

// csxIssueVerdictRequest closes one report.
type csxIssueVerdictRequest struct {
	ID      int64  `json:"id"`
	Verdict string `json:"verdict"`
}

// csxIssueCanonicalRequest attaches the bug a confirmed defect became.
type csxIssueCanonicalRequest struct {
	ID  int64  `json:"id"`
	Ref string `json:"ref"`
}

// csxIssueRequestLimit bounds a triage body. Both carry an id and one short
// string; anything larger is not a triage decision.
const csxIssueRequestLimit = 4 << 10

// setCSXIssueVerdict records an operator's decision about one report.
//
// The verdict vocabulary is closed and checked here, because a free-text
// verdict would make the channel's own aggregates meaningless — "confirmed"
// and "confirmed-csx-defect" would count as two outcomes.
//
// A verdict already given is kept. Reversing one silently would let a later
// operator overturn an earlier decision with nothing on the record, so the
// route reports what is stored rather than replacing it.
func (h *handler) setCSXIssueVerdict(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !byToken && !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	if h.csxIssues == nil {
		http.Error(w, "이 저장소는 제품 결함 신고 채널을 제공하지 않습니다", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, csxIssueRequestLimit)
	var input csxIssueVerdictRequest
	if err := decodeAdminJSON(r, &input); err != nil || input.ID <= 0 {
		http.Error(w, "신고 번호를 확인하세요", http.StatusBadRequest)
		return
	}
	verdict := strings.TrimSpace(input.Verdict)
	if !domain.ValidCSXIssueVerdict(verdict) {
		http.Error(w, "평결 값을 확인하세요", http.StatusBadRequest)
		return
	}

	applied, err := h.csxIssues.SetCSXIssueVerdict(r.Context(), input.ID, verdict, h.now().UTC())
	if err != nil {
		http.Error(w, "평결을 기록하지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	// applied=false is not a failure: it means this report already carries a
	// decision, which is exactly what an operator clicking twice should see.
	writeAdminJSON(w, http.StatusOK, map[string]any{"applied": applied})
}

// linkCSXIssueCanonical attaches the bug reference a confirmed defect became.
//
// It is what turns a repeat report into an answer: RecordCSXIssueReport hands
// a duplicate reporter the stored row including this ref, so the second agent
// to hit a known defect is told which bug it is rather than filing another.
//
// The store refuses to link anything not confirmed, and this route does not
// work around that — it reports the refusal instead.
func (h *handler) linkCSXIssueCanonical(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	byToken, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	if !byToken && !h.validAdminMutation(r) {
		http.Error(w, "허용되지 않은 요청입니다", http.StatusForbidden)
		return
	}
	if h.csxIssues == nil {
		http.Error(w, "이 저장소는 제품 결함 신고 채널을 제공하지 않습니다", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, csxIssueRequestLimit)
	var input csxIssueCanonicalRequest
	if err := decodeAdminJSON(r, &input); err != nil || input.ID <= 0 {
		http.Error(w, "신고 번호를 확인하세요", http.StatusBadRequest)
		return
	}
	ref := strings.TrimSpace(input.Ref)
	if ref == "" || len(ref) > 128 {
		http.Error(w, "버그 참조를 확인하세요", http.StatusBadRequest)
		return
	}

	linked, err := h.csxIssues.LinkCSXIssueCanonical(r.Context(), input.ID, ref)
	if err != nil {
		http.Error(w, "버그 참조를 연결하지 못했습니다", http.StatusServiceUnavailable)
		return
	}
	writeAdminJSON(w, http.StatusOK, map[string]any{"linked": linked})
}
