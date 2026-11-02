package evidence

import (
	"encoding/json"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"sync"
	"time"
)

type DeliveryDiagnostic struct {
	AttemptID     string `json:"attemptId"`
	RequestID     string `json:"requestId"`
	Outcome       string `json:"outcome"`
	HTTPStatus    int    `json:"httpStatus"`
	Accepted      int    `json:"accepted"`
	Rejected      int    `json:"rejected"`
	ElapsedMillis int64  `json:"elapsedMillis"`
	RecordedAt    string `json:"recordedAt"`
}
type deliveryState struct {
	mu           sync.Mutex
	attempt, ack string
}

func (b *Batcher) clearDeliveryDiagnostics() {
	b.delivery.mu.Lock()
	defer b.delivery.mu.Unlock()
	b.delivery.attempt = ""
	b.delivery.ack = ""
}

// DeliveryDiagnostics retains only this upload's latest attempt and positive ACK.
// Existing meta slots persist these bounded, payload-free records afterwards.
func (b *Batcher) DeliveryDiagnostics() (string, string) {
	b.delivery.mu.Lock()
	defer b.delivery.mu.Unlock()
	return b.delivery.attempt, b.delivery.ack
}
func (b *Batcher) recordDelivery(d DeliveryDiagnostic) {
	if !domain.IsDiagnosticRequestID(d.AttemptID) {
		d.AttemptID = "unknown"
	}
	if !domain.IsDiagnosticRequestID(d.RequestID) {
		d.RequestID = "unknown"
	}
	if d.ElapsedMillis < 0 {
		d.ElapsedMillis = 0
	}
	if d.ElapsedMillis > 86400000 {
		d.ElapsedMillis = 86400000
	}
	switch d.Outcome {
	case "ack", "ack_missing", "ack_invalid", "transport_error", "http_error":
	default:
		d.Outcome = "unknown"
	}
	if d.Accepted < 0 || d.Accepted > 500 {
		d.Accepted = -1
	}
	if d.Rejected < 0 || d.Rejected > 500 {
		d.Rejected = -1
	}
	raw, _ := json.Marshal(d)
	if len(raw) > 512 {
		return
	}
	b.delivery.mu.Lock()
	defer b.delivery.mu.Unlock()
	b.delivery.attempt = string(raw)
	if d.Outcome == "ack" && d.Accepted > 0 {
		b.delivery.ack = string(raw)
	}
}
func deliveryElapsed(start time.Time) int64 { return time.Since(start).Milliseconds() }
