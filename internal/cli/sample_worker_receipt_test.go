package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type workReceiptTransport func(*http.Request) (*http.Response, error)

func (f workReceiptTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type workReceiptReadError struct{}

func (workReceiptReadError) Read([]byte) (int, error) { return 0, errors.New("secret read failure") }
func (workReceiptReadError) Close() error             { return nil }

func TestSampleWorkerWorkRejectionReceipt(t *testing.T) {
	const id = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name                        string
		status                      int
		id, reason                  string
		body                        io.ReadCloser
		wantID, wantReason, failure string
	}{
		{"503", 503, id, "claim.deadline", io.NopCloser(strings.NewReader("secret response")), id, "claim.deadline", "http_status"},
		{"401 missing headers", 401, "", "", io.NopCloser(strings.NewReader("secret token")), "unknown", "unknown", "http_status"},
		{"header injection", 503, id + "\nsecret", "claim.deadline\nsecret", io.NopCloser(strings.NewReader("secret")), "unknown", "unknown", "http_status"},
		{"untrusted values", 500, strings.Repeat("A", 32), "secret", io.NopCloser(strings.NewReader("secret")), "unknown", "unknown", "http_status"},
		{"read failure", 200, id, "storage_unavailable", workReceiptReadError{}, id, "storage_unavailable", "body_read"},
		{"oversize", 200, id, "candidate_scan.pool_busy", io.NopCloser(strings.NewReader(strings.Repeat("x", sampleWorkerResponseLimit+1))), id, "candidate_scan.pool_busy", "body_too_large"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldClient, oldErr := sampleWorkerClient, sampleWorkerStderr
			t.Cleanup(func() { sampleWorkerClient, sampleWorkerStderr = oldClient, oldErr })
			var stderr bytes.Buffer
			sampleWorkerStderr = &stderr
			sampleWorkerClient = &http.Client{Transport: workReceiptTransport(func(r *http.Request) (*http.Response, error) {
				h := make(http.Header)
				h.Set("X-CSX-Request-ID", tc.id)
				h.Set("X-CSX-503-Reason", tc.reason)
				return &http.Response{StatusCode: tc.status, Header: h, Body: tc.body}, nil
			})}
			_, code := sampleWorkerRequestWork(context.Background(), "https://example.invalid", "secret bearer", map[string]any{"secret": "envelope"})
			log := stderr.String()
			if code != 1 || !strings.Contains(log, "request_id="+tc.wantID+" reason="+tc.wantReason+" failure="+tc.failure+" at_kst=") {
				t.Fatalf("code=%d log=%q", code, log)
			}
			if strings.Contains(log, "secret") || strings.Count(log, "\n") != 1 {
				t.Fatalf("unsafe diagnostic %q", log)
			}
			stamp := strings.TrimSpace(strings.Split(log, "at_kst=")[1])
			parsed, err := time.Parse(time.RFC3339, stamp)
			if err != nil {
				t.Fatal(err)
			}
			_, offset := parsed.Zone()
			if offset != 9*3600 {
				t.Fatalf("not KST: %s", stamp)
			}
		})
	}
}

func TestSampleWorkerWorkSuccessHasNoRejectionReceipt(t *testing.T) {
	oldClient, oldErr := sampleWorkerClient, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStderr = oldClient, oldErr })
	var stderr bytes.Buffer
	sampleWorkerStderr = &stderr
	sampleWorkerClient = &http.Client{Transport: workReceiptTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"decision":"NO_WORK"}`))}, nil
	})}
	_, code := sampleWorkerRequestWork(context.Background(), "https://example.invalid", "secret", nil)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
