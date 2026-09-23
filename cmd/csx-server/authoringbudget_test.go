package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The report reads the documented read-only dumps: psql COPY rows that are
// base64 per line, or plain JSON lines. It never needs CSX_DSN.
func TestAuthoringBudgetReportReadsDumps(t *testing.T) {
	t.Setenv("CSX_DSN", "")
	dir := t.TempDir()
	ledger := `{"ecosystem":"npm","name":"left-pad","version":"1.3.0","symbol":"","ledger":{"attempts":2,"authored":1,"history":[` +
		`{"at":"2026-09-01T00:00:00Z","kind":"WANTED","sessionId":"s1","outcome":"HANDED_OUT"},` +
		`{"at":"2026-09-01T00:50:00Z","kind":"WANTED","sessionId":"s2","outcome":"HANDED_OUT"},` +
		`{"at":"2026-09-01T00:54:00Z","kind":"WANTED","sessionId":"s2","outcome":"AUTHORED"}]}}`
	ledgerPath := filepath.Join(dir, "ledger.b64")
	if err := os.WriteFile(ledgerPath, []byte(base64.StdEncoding.EncodeToString([]byte(ledger))+"\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionsPath := filepath.Join(dir, "sessions.jsonl")
	sessions := `{"sessionId":"s1","label":"farm-1-slot1","computerName":"farm-1"}` + "\n" +
		`{"sessionId":"s2","label":"farm-1-slot2","computerName":null}` + "\n"
	if err := os.WriteFile(sessionsPath, []byte(sessions), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"authoring-budget-report", "--ledger", ledgerPath, "--sessions", sessionsPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var rep serverstore.AuthoringBudgetReport
	if err := json.Unmarshal(stdout.Bytes(), &rep); err != nil {
		t.Fatalf("output is not the report: %v\n%s", err, stdout.String())
	}
	if rep.Rows != 1 || rep.Overall.AttemptsToSuccess[2] != 1 || rep.MultiWriterEpisodesSinglePeer != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestAuthoringBudgetReportUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"authoring-budget-report"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want usage error 2", code)
	}
	if !strings.Contains(stderr.String(), "--ledger FILE --sessions FILE") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	bad := filepath.Join(t.TempDir(), "bad")
	if err := os.WriteFile(bad, []byte("not json, not base64!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{"authoring-budget-report", "--ledger", bad, "--sessions", bad}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d for a corrupt dump, want 1", code)
	}
	if !strings.Contains(stderr.String(), "bad:1") {
		t.Fatalf("stderr = %q, want the failing line", stderr.String())
	}
}
