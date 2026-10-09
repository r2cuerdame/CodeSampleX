package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
)

type cancelAfterEvidenceACK struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelAfterEvidenceACK) Close() error { b.cancel(); return b.ReadCloser.Close() }

type evidenceACKTransport func(*http.Request) (*http.Response, error)

func (f evidenceACKTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Shutdown or the overall drain deadline can follow an already-read ACK.
// Successful work must remain visible while unacknowledged rows stay pending.
func TestAcceptedEvidenceStatsSurviveCanceledDrain(t *testing.T) {
	home := newTestHome(t, func(cfg *config.Config) { cfg.Mode = config.ModeCommunity; cfg.ServerURL = "http://evidence.invalid" })
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for i := 0; i < 25; i++ {
		seedPendingObservation(t, d.DB, fmt.Sprintf("pkg:npm/cancel-ack-%02d@1.0.0", i), "call")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	acknowledged := 0
	d.HTTP = &http.Client{Transport: evidenceACKTransport(func(r *http.Request) (*http.Response, error) {
		var payload struct {
			Batches []json.RawMessage `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		acknowledged += len(payload.Batches)
		ack := fmt.Sprintf(`{"accepted":%d,"rejected":[]}`, len(payload.Batches))
		return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header),
			Body: cancelAfterEvidenceACK{io.NopCloser(strings.NewReader(ack)), cancel}}, nil
	})}
	sent, err := d.uploadNow(ctx)
	if sent == 0 || sent != acknowledged || !errors.Is(err, context.Canceled) {
		t.Fatalf("sent=%d ack=%d err=%v", sent, acknowledged, err)
	}
	last, ok, err := d.DB.GetStat(t.Context(), statLastUpload)
	if err != nil || !ok || last == "" {
		t.Fatalf("accepted ACK timestamp missing: last=%q ok=%v err=%v", last, ok, err)
	}
	total, ok, err := d.DB.GetStat(t.Context(), statEvidenceSent)
	if err != nil || !ok || total != strconv.Itoa(acknowledged) {
		t.Fatalf("accepted total=%q ok=%v err=%v; want %d", total, ok, err, acknowledged)
	}
	pending, err := d.DB.PendingObservationCount(t.Context(), 100)
	if err != nil || pending != 25-acknowledged {
		t.Fatalf("pending=%d err=%v; want %d", pending, err, 25-acknowledged)
	}
}
