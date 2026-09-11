package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The canonical release must roll clients before deploying the server. The
// previous server rejects the new outcome before touching a session or claim.
// Only that exact response can safely use the old, non-terminal classification.
func TestSampleWorkerUnsupportedOutcomeAcrossServerUpgrade(t *testing.T) {
	const token = "csx_author_v1_compat-secret"
	const rejected = `{"error":"unsupported authoring outcome"}`
	const released = `{"status":"RELEASED","work":{"package":"pkg:pub/path_provider@2.1.5","symbol":"getApplicationDocumentsDirectory"}}`
	for _, tc := range []struct {
		name         string
		outcome      string
		firstStatus  int
		firstBody    string
		secondStatus int
		secondBody   string
		wantCalls    int
		wantCode     int
		wantLegacy   bool
		wantNoClaim  bool
	}{
		{name: "new server keeps terminal measurement", firstStatus: 200, firstBody: released, wantCalls: 1},
		{name: "new server has no claim", firstStatus: 200, firstBody: `{"status":"NO_CLAIM"}`, wantCalls: 1, wantNoClaim: true},
		{name: "old server accepts legacy report", firstStatus: 400, firstBody: rejected, secondStatus: 200, secondBody: released, wantCalls: 2, wantLegacy: true},
		{name: "old server has no claim", firstStatus: 400, firstBody: rejected, secondStatus: 200, secondBody: `{"status":"NO_CLAIM"}`, wantCalls: 2, wantLegacy: true, wantNoClaim: true},
		{name: "legacy retry denied", firstStatus: 400, firstBody: rejected, secondStatus: 401, secondBody: `{"error":"authoring session unavailable"}`, wantCalls: 2, wantCode: 1, wantLegacy: true},
		{name: "no third attempt", firstStatus: 400, firstBody: rejected, secondStatus: 400, secondBody: rejected, wantCalls: 2, wantCode: 1, wantLegacy: true},
		{name: "axis restriction", firstStatus: 400, firstBody: `{"error":"no-callable-symbol and unsupported-environment apply only to Sample work"}`, wantCalls: 1, wantCode: 1},
		{name: "bad schema", firstStatus: 400, firstBody: `{"error":"invalid authoring outcome"}`, wantCalls: 1, wantCode: 1},
		{name: "unauthorized", firstStatus: 401, firstBody: rejected, wantCalls: 1, wantCode: 1},
		{name: "forbidden", firstStatus: 403, firstBody: rejected, wantCalls: 1, wantCode: 1},
		{name: "lease conflict", firstStatus: 409, firstBody: rejected, wantCalls: 1, wantCode: 1},
		{name: "server failure", firstStatus: 503, firstBody: rejected, wantCalls: 1, wantCode: 1},
		{name: "malformed rejection", firstStatus: 400, firstBody: "unsupported authoring outcome", wantCalls: 1, wantCode: 1},
		{name: "oversize rejection", firstStatus: 400, firstBody: rejected + strings.Repeat(" ", sampleWorkerResponseLimit), wantCalls: 1, wantCode: 1},
		{name: "extra error object", firstStatus: 400, firstBody: rejected + rejected, wantCalls: 1, wantCode: 1},
		{name: "different error schema", firstStatus: 400, firstBody: `{"error":"unsupported authoring outcome","status":"denied"}`, wantCalls: 1, wantCode: 1},
		{name: "other classification unchanged", outcome: "no-callable-symbol", firstStatus: 400, firstBody: rejected, wantCalls: 1, wantCode: 1},
		{name: "empty success", firstStatus: 200, firstBody: `{}`, wantCalls: 1, wantCode: 1},
		{name: "unknown success status", firstStatus: 200, firstBody: `{"status":"PENDING","work":{"package":"pkg:pub/path_provider@2.1.5"}}`, wantCalls: 1, wantCode: 1},
		{name: "legacy empty success", firstStatus: 400, firstBody: rejected, secondStatus: 200, secondBody: `{}`, wantCalls: 2, wantCode: 1, wantLegacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
			t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
			calls := 0
			outcome := tc.outcome
			if outcome == "" {
				outcome = "unsupported-environment"
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/work/outcome" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Content-Type") != "application/json" {
					t.Error("report request changed endpoint, method, or credentials")
				}
				var request struct {
					SchemaVersion int    `json:"schemaVersion"`
					Outcome       string `json:"outcome"`
					Detail        string `json:"detail"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				expectedOutcome := sampleWorkerOutcomes[outcome]
				expectedDetail := "pub@1 lacks Flutter SDK"
				if calls == 2 {
					expectedOutcome = "INFRASTRUCTURE"
					expectedDetail = "unsupported-environment (legacy server): " + expectedDetail
				}
				if request.SchemaVersion != 1 || request.Outcome != expectedOutcome || request.Detail != expectedDetail {
					t.Errorf("request %d = %+v, want %s with measured detail", calls, request, expectedOutcome)
				}
				if calls > tc.wantCalls {
					t.Errorf("unexpected retry %d", calls)
				}
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 {
					w.WriteHeader(tc.firstStatus)
					fmt.Fprint(w, tc.firstBody)
				} else {
					w.WriteHeader(tc.secondStatus)
					fmt.Fprint(w, tc.secondBody)
				}
			}))
			defer srv.Close()
			sampleWorkerClient = srv.Client()
			var out, stderr bytes.Buffer
			sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
			code := sampleWorkerMain(context.Background(), []string{"report", "--server", srv.URL, "--token", token, "--outcome", outcome, "--detail", "  pub@1 lacks Flutter SDK  "})
			if code != tc.wantCode || calls != tc.wantCalls {
				t.Fatalf("code=%d calls=%d stderr=%s", code, calls, stderr.String())
			}
			if strings.Contains(out.String()+stderr.String(), token) {
				t.Error("session token leaked to output")
			}
			if strings.Contains(stderr.String(), "retrying once as legacy INFRASTRUCTURE") != tc.wantLegacy {
				t.Errorf("compatibility notice=%q", stderr.String())
			}
			if tc.wantCode != 0 {
				if out.Len() != 0 {
					t.Errorf("failed report claimed success: %s", out.String())
				}
			} else if tc.wantNoClaim {
				if !strings.Contains(out.String(), "NO_CLAIM") || strings.Contains(out.String(), "Released") {
					t.Errorf("output=%q", out.String())
				}
			} else {
				accepted := "UNSUPPORTED_ENVIRONMENT"
				if tc.wantLegacy {
					accepted = "INFRASTRUCTURE"
				}
				if !strings.Contains(out.String(), " as "+accepted+".") {
					t.Errorf("output did not name accepted classification: %q", out.String())
				}
			}
		})
	}
}

func TestSampleWorkerUnsupportedOutcomeDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	for _, partialResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_response_%v", partialResponse), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if partialResponse {
					const body = `{"error":"unsupported authoring outcome"}`
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Content-Length", fmt.Sprint(len(body)+10))
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, body)
					// Valid JSON followed by unexpected EOF still fails closed.
					return
				}
				// The server may have consumed the report before disconnecting.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			}))
			defer srv.Close()
			oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
			t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
			sampleWorkerClient = srv.Client()
			var out, stderr bytes.Buffer
			sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
			code := sampleWorkerMain(context.Background(), []string{"report", "--server", srv.URL, "--token", "csx_author_v1_x", "--outcome", "unsupported-environment"})
			if code != 1 || calls != 1 || out.Len() != 0 || strings.Contains(stderr.String(), "retrying") {
				t.Fatalf("code=%d calls=%d out=%q stderr=%q", code, calls, out.String(), stderr.String())
			}
		})
	}
}
