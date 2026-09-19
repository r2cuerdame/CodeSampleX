package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

func captureFixClaims(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	oldOut, oldErr, oldClient, oldCollector := fixClaimsStdout, fixClaimsStderr, fixClaimsClient, fixClaimsCollector
	fixClaimsStdout, fixClaimsStderr = stdout, stderr
	t.Cleanup(func() {
		fixClaimsStdout, fixClaimsStderr, fixClaimsClient, fixClaimsCollector = oldOut, oldErr, oldClient, oldCollector
	})
	return stdout, stderr
}

const fixClaimsReleasesFixture = `[{"tag_name":"v2.4.1","html_url":"https://github.com/acme/foo/releases/tag/v2.4.1","published_at":"2026-09-10T00:00:00Z","body":"* fix: crash in ` + "`parse()`" + ` when the input buffer is empty (#512)\n* docs: fix typo\n"},
{"tag_name":"v2.4.0","html_url":"https://github.com/acme/foo/releases/tag/v2.4.0","published_at":"2026-09-01T00:00:00Z","body":"* feat: streaming\n"}]`

func TestFixClaimsCollectWritesASubmittableDocument(t *testing.T) {
	stdout, stderr := captureFixClaims(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixClaimsReleasesFixture))
	}))
	defer gh.Close()
	fixClaimsCollector = &fixclaims.Collector{Client: gh.Client(), BaseURL: gh.URL}
	seeds := filepath.Join(t.TempDir(), "seeds.json")
	if err := os.WriteFile(seeds, []byte(`{"sources":[{"ecosystem":"npm","name":"foo","repo":"acme/foo"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "candidates.json")
	if code := fixClaimsMain(context.Background(), []string{"collect", "--seeds", seeds, "--releases", "5", "--out", out}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SchemaVersion int                   `json:"schemaVersion"`
		Candidates    []fixclaims.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.SchemaVersion != 1 || len(doc.Candidates) != 1 {
		t.Fatalf("document: %v %s", err, raw)
	}
	if doc.Candidates[0].ClaimedFixedVersion != "2.4.1" || doc.Candidates[0].ClaimedBadVersion != "2.4.0" {
		t.Fatalf("%+v", doc.Candidates[0])
	}
	if !strings.Contains(stderr.String(), "npm/foo: 2 releases, 1 candidates") || stdout.Len() != 0 {
		t.Fatalf("stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if code := fixClaimsMain(context.Background(), []string{"collect", "--repo", "acme/foo"}); code != 2 {
		t.Fatalf("incomplete flags exit %d", code)
	}
}

func TestFixClaimsWorkerCommandsTalkToTheServer(t *testing.T) {
	stdout, stderr := captureFixClaims(t)
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer csx_author_v1_tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := readAllBody(r)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+body)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/fix-claims/candidates":
			_, _ = w.Write([]byte(`{"accepted":[{"index":0,"id":7,"status":"CLAIMED_FIX"}],"rejected":[]}`))
		case "/v1/fix-claims/work/next":
			_, _ = w.Write([]byte(`{"status":"ASSIGNED","work":{"id":7,"probes":[{"version":"2.4.0","environment":{"os":"linux"},"reason":"bad-version"}]}}`))
		case "/v1/fix-claims/7/runs":
			_, _ = w.Write([]byte(`{"status":"RECORDED","record":{"status":"VERIFIED_FIX"},"nextProbes":[]}`))
		case "/v1/fix-claims/7/outcome":
			_, _ = w.Write([]byte(`{"status":"RELEASED","record":{"status":"CLAIMED_FIX"}}`))
		case "/v1/fix-claims/metrics":
			_, _ = w.Write([]byte(`{"accepted":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	fixClaimsClient = srv.Client()
	t.Setenv(sampleWorkerSessionTokenEnv, "csx_author_v1_tok")

	dir := t.TempDir()
	candidates := filepath.Join(dir, "c.json")
	good := `{"schemaVersion":1,"ecosystem":"npm","name":"foo","claimedBadVersion":"2.4.0","claimedFixedVersion":"2.4.1","claim":"Fixed crash when parse() receives an empty buffer","sourceUrl":"https://github.com/acme/foo/releases/tag/v2.4.1","sourceType":"release_note","symbols":["parse"],"confidence":"high"}`
	bad := strings.Replace(good, `"confidence":"high"`, `"confidence":"sure"`, 1)
	if err := os.WriteFile(candidates, []byte(`{"schemaVersion":1,"candidates":[`+good+`,`+bad+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := fixClaimsMain(context.Background(), []string{"submit", candidates, "--server", srv.URL}); code != 0 {
		t.Fatalf("submit exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "candidate 1 refused locally: invalid-confidence") || !strings.Contains(stdout.String(), "submitted 1 candidates: 1 accepted, 0 rejected") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if len(seen) != 1 || !strings.Contains(seen[0], `"candidates":[{"schemaVersion":1`) || strings.Contains(seen[0], `"sure"`) {
		t.Fatalf("submitted: %v", seen)
	}

	stdout.Reset()
	if code := fixClaimsMain(context.Background(), []string{"next", "--os", "linux", "--server", srv.URL}); code != 0 {
		t.Fatalf("next exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(seen[1], `"verifierOS":["linux"]`) || !strings.Contains(stdout.String(), `"ASSIGNED"`) {
		t.Fatalf("next: %v %s", seen[1], stdout.String())
	}

	runs := filepath.Join(dir, "runs.json")
	if err := os.WriteFile(runs, []byte(`{"runs":[{"version":"2.4.0","environment":{"os":"linux"},"verdict":"FAIL","failureFingerprint":"`+strings.Repeat("a", 64)+`","receiptId":"r1","sampleId":"s1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := fixClaimsMain(context.Background(), []string{"runs", "--id", "7", runs, "--server", srv.URL}); code != 0 {
		t.Fatalf("runs exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(seen[2], "/v1/fix-claims/7/runs") || !strings.Contains(seen[2], `"schemaVersion":1`) || !strings.Contains(stdout.String(), "VERIFIED_FIX") {
		t.Fatalf("runs: %v %s", seen[2], stdout.String())
	}

	if code := fixClaimsMain(context.Background(), []string{"report", "--id", "7", "--outcome", "no-reproducer", "--detail", "nothing builds", "--server", srv.URL}); code != 0 {
		t.Fatalf("report exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(seen[3], `"outcome":"NO_REPRODUCER"`) {
		t.Fatalf("report: %v", seen[3])
	}
	stdout.Reset()
	if code := fixClaimsMain(context.Background(), []string{"metrics", "--server", srv.URL}); code != 0 || !strings.Contains(stdout.String(), `"accepted": 1`) {
		t.Fatalf("metrics exit %d: %s %s", code, stdout.String(), stderr.String())
	}
	// No token, no request.
	t.Setenv(sampleWorkerSessionTokenEnv, "")
	if code := fixClaimsMain(context.Background(), []string{"next", "--os", "linux", "--server", srv.URL}); code != 2 || len(seen) != 5 {
		t.Fatalf("tokenless poll: exit %d, %d requests", code, len(seen))
	}
}

func TestFixClaimsPromptRendersTheAGYContract(t *testing.T) {
	stdout, stderr := captureFixClaims(t)
	in := filepath.Join(t.TempDir(), "in.json")
	if err := os.WriteFile(in, []byte(`{"source":{"ecosystem":"pypi","name":"requests","repo":"psf/requests"},"release":"2.32.4","releaseUrl":"https://github.com/psf/requests/releases/tag/v2.32.4","line":"fix: decode error on redirects"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := fixClaimsMain(context.Background(), []string{"prompt", in}); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Never include a status") || !strings.Contains(stdout.String(), "psf/requests") {
		t.Fatalf("%s", stdout.String())
	}
}

func readAllBody(r *http.Request) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.String(), err
}
