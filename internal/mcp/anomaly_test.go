package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func localMachine() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.1", Libc: "musl",
	}
}

func concreteMismatch() domain.AnomalyReport {
	return domain.AnomalyReport{
		SchemaVersion: domain.AnomalyReportSchemaVersion,
		AnomalyType:   domain.AnomalyCSXPassLocalFail,
		Package:       "pkg:npm/axios@1.12.0",
		Symbol:        "axios.post",
		SampleID:      "sha256:" + strings.Repeat("a", 64),
		CSXObserved:   domain.AnomalyObservation{Result: "PASS", Stage: "contract"},
		LocalObserved: domain.AnomalyObservation{Result: "FAIL", Stage: "test"},
		Reproducible:  "yes",
		Confidence:    "high",
	}
}

// Nothing identifying may reach the wire, and the last place it can be
// stopped is here.
func TestPreparingAReportStripsRawOutputBeforeItCanBeSent(t *testing.T) {
	report := concreteMismatch()
	report.LocalObserved.Detail = `failed while reading C:\Users\ana\acme\src\pay.ts`
	report.LLMHypothesis = "the runbook at https://internal.acme.corp/x says the same"
	raw := "Error: ENOENT: no such file or directory, open '/home/ana/acme/.env'\n" +
		"  token=Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A"

	prepared, redacted, err := PrepareAnomalyReport(report, raw, localMachine())
	if err != nil {
		t.Fatalf("a concrete mismatch was refused: %v", err)
	}
	if !redacted {
		t.Fatal("redaction was not reported")
	}
	blob := string(domain.MustCanonicalJSON(prepared))
	for _, secret := range []string{"ana", "acme", "pay.ts", ".env", "Zq7bY2mK9pW3nT6vR8sL1xC5dH0gJe4A", "internal.acme.corp"} {
		if strings.Contains(blob, secret) {
			t.Fatalf("%q would have been sent: %s", secret, blob)
		}
	}
	if prepared.ErrorCode == "" || prepared.ErrorFingerprint == "" {
		t.Fatalf("the sanitizer's usable output was lost with the raw text: %+v", prepared)
	}
	// And the dimensions no agent can be expected to state were filled in
	// from the machine, because a report about musl that does not say musl
	// is a report about nothing.
	if prepared.Environment.Libc != "musl" {
		t.Fatalf("environment = %+v", prepared.Environment)
	}
}

func TestPreparingRefusesAReportWithNothingMeasured(t *testing.T) {
	report := concreteMismatch()
	report.LocalObserved = domain.AnomalyObservation{Detail: "this looks wrong"}
	if _, _, err := PrepareAnomalyReport(report, "", localMachine()); err == nil {
		t.Fatal("a report with no local PASS/FAIL was accepted")
	}
}

// A log pasted into a detail field is a log that got past the placeholders
// somewhere.
func TestPreparingBoundsTheErrorTemplate(t *testing.T) {
	report := concreteMismatch()
	prepared, _, err := PrepareAnomalyReport(report, strings.Repeat("boom ", 2000), localMachine())
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.ErrorTemplate) > maxErrorTemplate+20 {
		t.Fatalf("error template is %d bytes; it is meant to be a core, not a log", len(prepared.ErrorTemplate))
	}
}

func TestSubmitSendsTheEnvelopeAndReturnsTheServersAnswer(t *testing.T) {
	var got anomalyEnvelope
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/anomalies" {
			t.Errorf("posted to %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reportId":7,"status":"accepted","verificationState":"queued for verification",` +
			`"verificationJobId":42,"note":"VERIFICATION PENDING. Nothing has been confirmed."}`))
	}))
	defer srv.Close()

	out, err := SubmitAnomalyReport(context.Background(), srv.Client(), srv.URL,
		"2026-08-24", "anon-1", concreteMismatch())
	if err != nil {
		t.Fatal(err)
	}
	if out.ReportID != 7 || out.Status != "accepted" || out.VerificationJobID != 42 {
		t.Fatalf("submission = %+v", out)
	}
	if got.Epoch != "2026-08-24" || got.AnonID != "anon-1" || got.SchemaVersion != 1 {
		t.Fatalf("envelope = %+v", got)
	}
	if got.Report.Package != "pkg:npm/axios@1.12.0" {
		t.Fatalf("report did not travel: %+v", got.Report)
	}
}

func TestSubmitSurfacesTheServersRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"localObserved.result must be PASS or FAIL"}`))
	}))
	defer srv.Close()

	_, err := SubmitAnomalyReport(context.Background(), srv.Client(), srv.URL, "e", "a", concreteMismatch())
	if err == nil || !strings.Contains(err.Error(), "localObserved") {
		t.Fatalf("the caller was not told why: %v", err)
	}
}

// The failure mode this channel actually has is not a lost report. It is an
// agent telling its user the bug is fixed.
func TestTheToolNeverLetsAReportReadAsAConfirmation(t *testing.T) {
	deps := emptyDeps()
	deps.ReportAnomaly = func(context.Context, domain.AnomalyReport, string) (AnomalySubmission, error) {
		return AnomalySubmission{
			ReportID:          9,
			Status:            "accepted",
			ReportStatus:      domain.AnomalyStatusQueued,
			VerificationState: "queued for verification",
			VerificationJobID: 3,
			Note:              "VERIFICATION PENDING. This is a verification request, not a finding.",
		}, nil
	}
	c := startServer(t, deps)
	res := result(t, c.call(1, "tools/call", map[string]any{
		"name": "report_anomaly",
		"arguments": map[string]any{
			"anomalyType":   domain.AnomalyCSXPassLocalFail,
			"package":       "pkg:npm/axios@1.12.0",
			"csxObserved":   map[string]any{"result": "PASS"},
			"localObserved": map[string]any{"result": "FAIL", "stage": "test"},
		},
	}))
	structured, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("the answer must travel in structuredContent: %v", res)
	}
	if structured["confirmed"] != false {
		t.Fatalf("confirmed = %v, want false", structured["confirmed"])
	}
	note, _ := structured["note"].(string)
	if !strings.Contains(note, "VERIFICATION PENDING") {
		t.Fatalf("note = %q", note)
	}
	if structured["reportId"].(float64) != 9 {
		t.Fatalf("reportId = %v", structured["reportId"])
	}
	// The text block says the same thing for a client that renders text.
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		t.Fatal("no text content")
	}
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "VERIFICATION PENDING") {
		t.Fatalf("text block = %q", text)
	}
}

// Local-only mode exists to send nothing, and a report is a thing that is
// sent. The refusal has to explain itself rather than look like a failure.
func TestTheLocalOnlyRefusalExplainsItself(t *testing.T) {
	deps := emptyDeps()
	deps.ReportAnomaly = func(context.Context, domain.AnomalyReport, string) (AnomalySubmission, error) {
		return AnomalySubmission{}, ErrAnomalyLocalOnly
	}
	c := startServer(t, deps)
	res := result(t, c.call(1, "tools/call", map[string]any{
		"name": "report_anomaly",
		"arguments": map[string]any{
			"anomalyType":   domain.AnomalyCSXPassLocalFail,
			"package":       "pkg:npm/axios@1.12.0",
			"csxObserved":   map[string]any{"result": "PASS"},
			"localObserved": map[string]any{"result": "FAIL"},
		},
	}))
	if res["isError"] != true {
		t.Fatalf("a refusal must be reported as one: %v", res)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	text, _ := first["text"].(string)
	if !strings.Contains(text, "local-only") || !strings.Contains(text, "csx init --community") {
		t.Fatalf("the user is not told what would change it: %q", text)
	}
}
