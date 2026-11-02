package evidence

import (
	"bytes"
	"context"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestEvidenceDeliveryDiagnosticsClientCorrelatesWithoutBody(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	var attempt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt = r.Header.Get("X-CSX-Attempt-ID")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("X-CSX-Request-ID", "req_1234567890abcdef12345678")
		w.WriteHeader(202)
		_, _ = w.Write([]byte("{\"accepted\":1,\"rejected\":[]}"))
	}))
	defer srv.Close()
	b := &Batcher{}
	n, _, err := b.post(context.Background(), srv.Client(), srv.URL, []domain.ObservationBatch{{Package: "private-body-marker"}})
	if err != nil || n != 1 {
		t.Fatalf("unchanged ACK result n=%d err=%v", n, err)
	}
	if !regexp.MustCompile("^req_[0-9a-f]{24}$").MatchString(attempt) {
		t.Fatalf("missing bounded attempt id %q", attempt)
	}
	attemptRecord, ackRecord := b.DeliveryDiagnostics()
	got := attemptRecord + ackRecord
	if !strings.Contains(got, attempt) || !strings.Contains(got, "req_1234567890abcdef12345678") || !strings.Contains(got, "\"accepted\":1") {
		t.Fatalf("correlation missing: %s", got)
	}
	if strings.Contains(got, "private-body-marker") || strings.Contains(got, srv.URL) {
		t.Fatal("delivery log exposed body or URL")
	}
}
