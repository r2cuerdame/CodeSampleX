package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

func writeFixReproducer(t *testing.T, root, name string, doc map[string]any) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, fixReproducerFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "csx.json"), []byte(`{"schemaVersion":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

var fixWorkCandidate = fixclaims.Candidate{
	SchemaVersion: 1, Ecosystem: "npm", Name: "express", ClaimedBadVersion: "4.22.1", ClaimedFixedVersion: "4.22.2",
	Claim: "restore >20 array parsing for req.query repeated keys", SourceURL: "https://github.com/expressjs/express/releases/tag/v4.22.2",
	SourceType: fixclaims.SourceReleaseNote, Symbols: []string{"req.query"}, Confidence: fixclaims.ConfidenceHigh,
}

func TestFixClaimsWorkRunsEveryProbeAndFilesTheRuns(t *testing.T) {
	stdout, stderr := captureFixClaims(t)
	root := t.TempDir()
	writeFixReproducer(t, root, "express-query", map[string]any{
		"schemaVersion": 1, "source": "GENERATED", "candidate": fixWorkCandidate,
		"environments": []fixclaims.Environment{{OS: "linux", Runtime: "node", RuntimeVersion: "22"}},
	})
	other := fixWorkCandidate
	other.Name = "axios"
	other.Claim = "something else"

	var mu sync.Mutex
	var polls int
	var posted = map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := readAllBody(r)
		switch r.URL.Path {
		case "/v1/fix-claims/work/next":
			polls++
			var work map[string]any
			switch polls {
			case 1:
				work = map[string]any{"id": 7, "candidate": fixWorkCandidate, "probes": []fixclaims.Probe{
					{Version: "4.22.1", Environment: fixclaims.DefaultEnvironment, Reason: "bad-version"},
					{Version: "4.22.2", Environment: fixclaims.DefaultEnvironment, Reason: "fixed-version"},
				}}
			case 2:
				work = map[string]any{"id": 8, "candidate": other, "probes": []fixclaims.Probe{{Version: "1.0.0", Environment: fixclaims.DefaultEnvironment, Reason: "bad-version"}}}
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "NO_WORK"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ASSIGNED", "work": work})
		case "/v1/fix-claims/7/reproducer", "/v1/fix-claims/7/runs", "/v1/fix-claims/8/outcome":
			posted[r.URL.Path] = body
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "RECORDED", "record": map[string]any{"status": "VERIFIED_FIX", "pairOutcome": "REPRODUCED_AND_FIXED"}})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oldExec := fixClaimsExecute
	t.Cleanup(func() { fixClaimsExecute = oldExec })
	var executed []string
	fixClaimsExecute = func(_ context.Context, base, tok string, id int64, c fixclaims.Candidate, dir string, p fixclaims.Probe) (fixProbeResult, error) {
		if id != 7 {
			t.Errorf("executed under lease %d", id)
		}
		if !strings.HasSuffix(dir, "express-query") || c.Name != "express" {
			t.Errorf("executed %s for %s", dir, c.Name)
		}
		executed = append(executed, p.Environment.Key()+"@"+p.Version)
		run := fixclaims.Run{Version: p.Version, Environment: p.Environment, ReceiptID: "r-" + p.Version, SampleID: "s-" + p.Version}
		if p.Version == "4.22.1" {
			run.Verdict = fixclaims.VerdictFail
			run.FailureFingerprint = strings.Repeat("ab", 32)
			return fixProbeResult{Run: run, Detail: "contract failed"}, nil
		}
		run.Verdict = fixclaims.VerdictPass
		return fixProbeResult{Run: run, Detail: "contract passed"}, nil
	}
	fixClaimsClient = srv.Client()
	t.Setenv(sampleWorkerSessionTokenEnv, "tok")

	code := fixClaimsMain(context.Background(), []string{"work", "--repro-root", root, "--os", "linux", "--server", srv.URL})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	want := "linux|||@4.22.1,linux|||@4.22.2,linux||node|22@4.22.1,linux||node|22@4.22.2"
	if got := strings.Join(executed, ","); got != want {
		t.Fatalf("executed:\n got %s\nwant %s", got, want)
	}
	var runsDoc struct {
		SchemaVersion int             `json:"schemaVersion"`
		Runs          []fixclaims.Run `json:"runs"`
	}
	if err := json.Unmarshal([]byte(posted["/v1/fix-claims/7/runs"]), &runsDoc); err != nil || runsDoc.SchemaVersion != 1 || len(runsDoc.Runs) != 4 {
		t.Fatalf("runs: %v %s", err, posted["/v1/fix-claims/7/runs"])
	}
	if runsDoc.Runs[0].Verdict != fixclaims.VerdictFail || runsDoc.Runs[0].ReceiptID != "r-4.22.1" || runsDoc.Runs[1].Verdict != fixclaims.VerdictPass {
		t.Fatalf("runs: %+v", runsDoc.Runs)
	}
	if !strings.Contains(posted["/v1/fix-claims/7/reproducer"], `"source":"GENERATED"`) {
		t.Fatalf("reproducer: %s", posted["/v1/fix-claims/7/reproducer"])
	}
	if !strings.Contains(posted["/v1/fix-claims/8/outcome"], `"outcome":"NO_REPRODUCER"`) {
		t.Fatalf("outcome: %s", posted["/v1/fix-claims/8/outcome"])
	}
	out := stdout.String()
	for _, want := range []string{"lease 7: pkg:npm/express@4.22.2 4.22.1 -> 4.22.2", "recorded: status=VERIFIED_FIX pairOutcome=REPRODUCED_AND_FIXED",
		"lease 8:", "no reproducer; handed back", "NO_WORK after 2 lease(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestFixClaimsWorkReportsInfrastructureWhenNoProbeRan(t *testing.T) {
	_, stderr := captureFixClaims(t)
	root := t.TempDir()
	writeFixReproducer(t, root, "express-query", map[string]any{"schemaVersion": 1, "source": "UPSTREAM_REPRO", "candidate": fixWorkCandidate})
	var mu sync.Mutex
	var outcome string
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		body, _ := readAllBody(r)
		switch r.URL.Path {
		case "/v1/fix-claims/work/next":
			polls++
			if polls > 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "BUDGET_EXHAUSTED"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ASSIGNED", "work": map[string]any{"id": 3, "candidate": fixWorkCandidate,
				"probes": []fixclaims.Probe{{Version: "4.22.1", Environment: fixclaims.DefaultEnvironment, Reason: "bad-version"}}}})
		case "/v1/fix-claims/3/reproducer":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "RECORDED"})
		case "/v1/fix-claims/3/outcome":
			outcome = body
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "RELEASED"})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	oldExec := fixClaimsExecute
	t.Cleanup(func() { fixClaimsExecute = oldExec })
	fixClaimsExecute = func(context.Context, string, string, int64, fixclaims.Candidate, string, fixclaims.Probe) (fixProbeResult, error) {
		return fixProbeResult{}, errors.New("no container isolation on this host")
	}
	fixClaimsClient = srv.Client()
	if code := fixClaimsMain(context.Background(), []string{"work", "--repro-root", root, "--once", "--server", srv.URL, "--token", "tok"}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(outcome, `"outcome":"INFRASTRUCTURE"`) || !strings.Contains(outcome, "no container isolation") {
		t.Fatalf("outcome: %s", outcome)
	}
}

func TestLoadFixReproducersRefusesAHalfWrittenOne(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "broken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fixReproducerFile), []byte(`{"schemaVersion":1,"source":"GENERATED","candidate":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixReproducers(root); err == nil || !strings.Contains(err.Error(), "no csx.json") {
		t.Fatalf("err: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "csx.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fixReproducerFile), []byte(`{"schemaVersion":1,"source":"MODEL_SAID_SO","candidate":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFixReproducers(root); err == nil || !strings.Contains(err.Error(), "source must be") {
		t.Fatalf("err: %v", err)
	}
}

func TestFixRunFromReceiptOnlyTheContractDecides(t *testing.T) {
	probe := fixclaims.Probe{Version: "1.0.0", Environment: fixclaims.DefaultEnvironment}
	base := domain.VerificationReceipt{SchemaVersion: 2, SampleID: "sha256:s", Stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS", "load": "PASS"}}

	run, detail := fixRunFromReceipt(base, nil, probe)
	if run.Verdict != fixclaims.VerdictPass || run.SampleID != "sha256:s" || run.ReceiptID == "" || detail != "contract passed" {
		t.Fatalf("pass: %+v %s", run, detail)
	}

	failed := base
	failed.Stages = map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "FAIL", "load": "SKIPPED"}
	failed.StageFailures = map[string]domain.FailureEvidence{"contract": {Fingerprint: "sha256:" + strings.Repeat("cd", 32), ErrorSummary: "AssertionError: expected array"}}
	run, detail = fixRunFromReceipt(failed, nil, probe)
	if run.Verdict != fixclaims.VerdictFail || run.FailureFingerprint != strings.Repeat("cd", 32) || !strings.Contains(detail, "AssertionError") {
		t.Fatalf("fail: %+v %s", run, detail)
	}

	broken := base
	broken.Stages = map[string]string{"resolve": "FAIL", "compile": sandbox.ResultSkipped, "contract": sandbox.ResultSkipped, "load": sandbox.ResultSkipped}
	run, detail = fixRunFromReceipt(broken, map[string]string{"resolve": "npm ERR! notarget No matching version found for express@9.9.9\n"}, probe)
	if run.Verdict != fixclaims.VerdictUnrunnable || run.ReceiptID != "" || run.SampleID != "" || !strings.Contains(detail, "resolve failed: npm ERR! notarget") {
		t.Fatalf("unrunnable: %+v %s", run, detail)
	}
}
