package httpapi

import (
	"context"
	"errors"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
	"log"
	"net/http"
	"time"
)

type evidenceDeliveryTrace struct{ id, attempt string }

func newEvidenceDeliveryTrace(w http.ResponseWriter, r *http.Request) evidenceDeliveryTrace {
	id := domain.NewDiagnosticRequestID()
	attempt := r.Header.Get("X-CSX-Attempt-ID")
	if !domain.IsDiagnosticRequestID(id) {
		id = "unknown"
	} else {
		w.Header().Set("X-CSX-Request-ID", id)
	}
	if !domain.IsDiagnosticRequestID(attempt) {
		attempt = "unknown"
	}
	return evidenceDeliveryTrace{id, attempt}
}
func (t evidenceDeliveryTrace) begin(stage string) func(bool, int, int) {
	started := time.Now()
	log.Printf("csx_evidence request_id=%s attempt_id=%s stage=%s state=begin elapsed_ms=0 accepted=0 rejected=0", t.id, t.attempt, stage)
	return func(ok bool, accepted, rejected int) {
		state := "failed"
		if ok {
			state = "ok"
		}
		if accepted < 0 || accepted > 500 {
			accepted = -1
		}
		if rejected < 0 || rejected > 500 {
			rejected = -1
		}
		elapsed := time.Since(started).Milliseconds()
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed > 86400000 {
			elapsed = 86400000
		}
		log.Printf("csx_evidence request_id=%s attempt_id=%s stage=%s state=%s elapsed_ms=%d accepted=%d rejected=%d", t.id, t.attempt, stage, state, elapsed, accepted, rejected)
	}
}

func (t evidenceDeliveryTrace) storeFailure(err error) {
	reason := "store_error"
	switch {
	case serverstore.IsPoolBusy(err):
		reason = "pool_busy"
	case serverstore.IsQueryTimeout(err):
		reason = "query_timeout"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "context_deadline"
	case errors.Is(err, context.Canceled):
		reason = "context_canceled"
	}
	log.Printf("csx_evidence request_id=%s attempt_id=%s stage=store state=failed reason=%s", t.id, t.attempt, reason)
}
