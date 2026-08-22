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

// A sample writer could take work and submit a sample. It had no way to say
// "there is nothing here to write", so the only thing the server ever learned
// about a hopeless coordinate was silence — and silence is what a busy worker
// looks like.

func TestSampleWorkerReportsWhyItCouldNotWriteASample(t *testing.T) {
	const token = "csx_author_v1_report-test"
	var body struct {
		SchemaVersion int    `json:"schemaVersion"`
		Outcome       string `json:"outcome"`
		Detail        string `json:"detail"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/authoring/work/outcome" ||
			r.Header.Get("Authorization") != "Bearer "+token {
			t.Fatalf("request = %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"RELEASED","work":{"package":"pkg:maven/g/a@1.0.0","symbol":""}}`)
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr

	code := sampleWorkerMain(context.Background(), []string{"report", "--server", srv.URL, "--token", token,
		"--outcome", "no-callable-symbol", "--detail", "pom-only artifact: no jar, no classes"})
	if code != 0 {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if body.SchemaVersion != 1 || body.Outcome != "NO_CALLABLE_SYMBOL" || body.Detail == "" {
		t.Fatalf("reported = %+v", body)
	}
	if !strings.Contains(out.String(), "pkg:maven/g/a@1.0.0") {
		t.Errorf("stdout did not name the released work: %q", out.String())
	}
}

// The short names exist because an agent types them. Anything else has to be
// refused locally rather than sent: an unrecognised classification is worse
// than none, because it would be recorded as evidence about the coordinate.
func TestSampleWorkerRefusesAnUnknownOutcomeWithoutCallingTheServer(t *testing.T) {
	const token = "csx_author_v1_report-test"
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("an unknown outcome reached the server")
	}))
	defer srv.Close()

	oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
	t.Cleanup(func() { sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr })
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr

	for _, outcome := range []string{"", "authored", "gave-up", "NO_CALLABLE_SYMBOLS"} {
		if code := sampleWorkerMain(context.Background(), []string{"report", "--server", srv.URL,
			"--token", token, "--outcome", outcome}); code == 0 {
			t.Errorf("outcome %q was accepted", outcome)
		}
	}
}

func TestSampleWorkerReportAcceptsEveryClassification(t *testing.T) {
	want := map[string]string{
		"infrastructure":     "INFRASTRUCTURE",
		"transient":          "TRANSIENT",
		"no-callable-symbol": "NO_CALLABLE_SYMBOL",
		"no-output":          "NO_OUTPUT",
	}
	for flag, wire := range want {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Outcome string `json:"outcome"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			got = body.Outcome
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"status":"NO_CLAIM"}`)
		}))
		oldClient, oldOut, oldErr := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr
		sampleWorkerClient = srv.Client()
		var out, stderr bytes.Buffer
		sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
		code := sampleWorkerMain(context.Background(), []string{"report", "--server", srv.URL,
			"--token", "csx_author_v1_x", "--outcome", flag})
		sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr = oldClient, oldOut, oldErr
		srv.Close()
		if code != 0 {
			t.Errorf("%s: exit = %d stderr=%s", flag, code, stderr.String())
		}
		if got != wire {
			t.Errorf("%s sent %q, want %q", flag, got, wire)
		}
	}
}
