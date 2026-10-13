package httpapi

import (
	"bytes"
	"encoding/json"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"log"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func TestEvidenceDeliveryDiagnosticsServerUsesFreshBoundedIDs(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	srv, _, _ := newTestServer(t, nil)
	payload, _ := json.Marshal(map[string]any{"batches": []domain.ObservationBatch{testBatch("pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"), domain.StageProjectCompile, domain.ResultPass, 10)}})
	previousID := ""
	for i := 0; i < 2; i++ {
		r, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evidence/batches", bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSX-Attempt-ID", "req_abcdef0123456789abcdef01")
		resp, err := srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		id := resp.Header.Get("X-CSX-Request-ID")
		if resp.StatusCode != 202 || !regexp.MustCompile("^req_[0-9a-f]{24}$").MatchString(id) || id == previousID {
			t.Fatalf("fresh request receipt missing status=%d id=%q", resp.StatusCode, id)
		}
		if !strings.Contains(logs.String(), "request_id="+id+" attempt_id=req_abcdef0123456789abcdef01") {
			t.Fatal("server correlation missing")
		}
		previousID = id
	}
	got := logs.String()
	for _, word := range []string{"axios.post", "pkg:npm/axios", "observationCount", "anonId"} {
		if strings.Contains(got, word) {
			t.Fatalf("log leaked %s", word)
		}
	}
	if !strings.Contains(got, "stage=store state=ok") || !strings.Contains(got, "accepted=1") {
		t.Fatal("actual store outcome not logged")
	}
}
func TestEvidenceDeliveryDiagnosticsRejectsUntrustedAttemptText(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	srv, _, _ := newTestServer(t, nil)
	r, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/evidence/batches", strings.NewReader("{}"))
	r.Header.Set("X-CSX-Attempt-ID", "Bearer private-header-marker /home/alice")
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("status changed %d", resp.StatusCode)
	}
	got := logs.String()
	if strings.Contains(got, "private-header-marker") || strings.Contains(got, "/home/alice") {
		t.Fatal("untrusted header logged")
	}
	if !strings.Contains(got, "attempt_id=unknown") {
		t.Fatal("invalid attempt id did not remain unknown")
	}
}

func TestEvidenceDeliveryDiagnosticsRefusalDoesNotLogInput(t *testing.T) {
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	srv, _, _ := newTestServer(t, nil)
	b := testBatch("private-package-marker", "private-symbol-marker", nodeEnv("esm"), domain.StageProjectCompile, domain.ResultPass, 1)
	var out ingestResponse
	resp := postJSON(t, srv.URL+"/v1/evidence/batches", map[string]any{"batches": []domain.ObservationBatch{b}}, &out)
	if resp.StatusCode != 202 || out.Accepted != 0 || len(out.Rejected) != 1 {
		t.Fatal("refusal semantics changed")
	}
	got := logs.String()
	if strings.Contains(got, "private-package-marker") || strings.Contains(got, "private-symbol-marker") {
		t.Fatal("refusal log exposed input")
	}
}
