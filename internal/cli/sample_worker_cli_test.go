package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The farm executor for CLI work.
//
// `csx sample-worker next` prints a CLI assignment as what it is -- a tool,
// a command pattern and an OS -- and `csx sample-worker cli-run` runs it:
// it measures the host's version of the tool first, hands the claim back
// as unsupported-environment when the host cannot provide the coordinate,
// and otherwise runs the command through the ordinary evidence path with
// farm provenance and uploads the observation.

func stubSampleWorker(t *testing.T, srv *httptest.Server) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	oldClient, oldOut, oldErr, oldCapability := sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability
	oldContainerOS := sampleWorkerContainerOS
	t.Cleanup(func() {
		sampleWorkerClient, sampleWorkerStdout, sampleWorkerStderr, sampleWorkerCapability = oldClient, oldOut, oldErr, oldCapability
		sampleWorkerContainerOS = oldContainerOS
	})
	sampleWorkerCapability = func(context.Context) domain.SandboxCapability { return domain.CapContainerRun }
	sampleWorkerContainerOS = func(context.Context) string { return "linux" }
	sampleWorkerClient = srv.Client()
	var out, stderr bytes.Buffer
	sampleWorkerStdout, sampleWorkerStderr = &out, &stderr
	return &out, &stderr
}

func cliAssignment(pkg, tool, version, command, targetOS string) string {
	lease := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	return fmt.Sprintf(`{"status":"ASSIGNED","work":{"package":%q,"ecosystem":"generic","name":"cli/%s","version":%q,"symbol":%q,"kind":"CLI","axis":"EVIDENCE","score":9,"tool":%q,"command":%q,"targetOS":%q,"leaseExpiresAt":%q}}`,
		pkg, tool, version, domain.EncodeCLIWorkSymbol(targetOS, command), tool, command, targetOS, lease.Format(time.RFC3339))
}

func TestSampleWorkerNextPrintsCLIWorkAsATool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cliAssignment("pkg:generic/cli/git@2.47.2", "git", "2.47.2", "worktree add <path>", "linux"))
	}))
	defer srv.Close()
	out, stderr := stubSampleWorker(t, srv)
	if code := sampleWorkerMain(context.Background(), []string{"next", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Assigned CLI coverage work", "Tool: git 2.47.2", "OS: linux", "Command: git worktree add <path>",
		"csx sample-worker cli-run", "unsupported-environment"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "csx sample propose") || strings.Contains(out.String(), "pin the exact package") {
		t.Errorf("CLI work printed package instructions:\n%s", out.String())
	}
}

// The probe is printed as what it is: no version yet, the version probe as
// the command.
func TestSampleWorkerNextPrintsAProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, cliAssignment("pkg:generic/cli/gh", "gh", "", "", "windows"))
	}))
	defer srv.Close()
	out, stderr := stubSampleWorker(t, srv)
	if code := sampleWorkerMain(context.Background(), []string{"next", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{"Tool: gh (version unknown: this is the probe)", "OS: windows", "Command: gh --version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

// cliRunServer is the farm-facing half of a server: it hands out one CLI
// assignment, records the outcome it is given, and accepts evidence.
type cliRunServer struct {
	*httptest.Server
	mu        sync.Mutex
	assign    string
	polls     []map[string]any
	outcomes  []map[string]any
	batches   []domain.ObservationBatch
	evidences int
}

func newCLIRunServer(t *testing.T, assign string) *cliRunServer {
	t.Helper()
	s := &cliRunServer{assign: assign}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		switch r.URL.Path {
		case "/v1/authoring/work/next":
			var envelope map[string]any
			_ = json.Unmarshal(raw, &envelope)
			s.polls = append(s.polls, envelope)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, s.assign)
		case "/v1/authoring/work/outcome":
			var outcome map[string]any
			_ = json.Unmarshal(raw, &outcome)
			s.outcomes = append(s.outcomes, outcome)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"status":"RELEASED","work":{"package":"pkg:generic/cli/x","symbol":"[linux]"}}`)
		case "/v1/evidence/batches":
			var body struct {
				Batches []domain.ObservationBatch `json:"batches"`
			}
			_ = json.Unmarshal(raw, &body)
			s.batches = append(s.batches, body.Batches...)
			s.evidences++
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func farmHome(t *testing.T, serverURL string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("CSX_HOME", home)
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = serverURL
	if err := cfg.Save(home); err != nil {
		t.Fatal(err)
	}
}

// The probe end to end: the host's git is measured, `git --version` runs
// through the evidence path, and the farm-provenance observation at the
// measured version lands on the server as a batch the server admits.
func TestSampleWorkerCLIRunRecordsAFarmProbe(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	have := environment.Probe(context.Background(), "git")
	if have == "" {
		t.Skip("git version not measurable")
	}
	srv := newCLIRunServer(t, cliAssignment("pkg:generic/cli/git", "git", "", "", runtime.GOOS))
	farmHome(t, srv.URL)
	out, stderr := stubSampleWorker(t, srv.Server)
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), out.String())
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.polls) != 1 || srv.polls[0]["hostOS"] != runtime.GOOS {
		t.Fatalf("polls = %+v; cli-run must name its host OS", srv.polls)
	}
	if len(srv.outcomes) != 0 {
		t.Fatalf("a filled coordinate was handed back: %+v", srv.outcomes)
	}
	var cli []domain.ObservationBatch
	for _, b := range srv.batches {
		if strings.HasPrefix(b.Package, "pkg:generic/cli/") {
			cli = append(cli, b)
		}
	}
	if len(cli) != 1 {
		t.Fatalf("uploaded CLI batches = %+v", srv.batches)
	}
	b := cli[0]
	if b.Package != "pkg:generic/cli/git@"+have || b.Symbol != "farm:--version" || b.Result != domain.ResultPass ||
		strings.ToLower(b.Environment.OS) != runtime.GOOS {
		t.Fatalf("batch = %+v", b)
	}
	if err := serverstore.ValidateBatch(b); err != nil {
		t.Fatalf("server would refuse the farm batch: %v", err)
	}
	var summary struct {
		Status, Tool, Version, Command, OS, Result string
		Uploaded                                   int
	}
	last := strings.TrimSpace(out.String())
	last = last[strings.LastIndex(last, "\n")+1:]
	if err := json.Unmarshal([]byte(last), &summary); err != nil || summary.Status != "RECORDED" || summary.Tool != "git" ||
		summary.Version != have || summary.OS != runtime.GOOS || summary.Result != "PASS" || summary.Uploaded < 1 {
		t.Fatalf("summary line %q -> %+v err=%v", last, summary, err)
	}
}

// A coordinate at a version this host does not have is not run against
// another version and quietly relabelled; it is handed back as a
// measurement of the environment, naming both versions.
func TestSampleWorkerCLIRunReportsAnotherVersionAsUnsupported(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	have := environment.Probe(context.Background(), "git")
	if have == "" {
		t.Skip("git version not measurable")
	}
	srv := newCLIRunServer(t, cliAssignment("pkg:generic/cli/git@0.0.1", "git", "0.0.1", "status --short", runtime.GOOS))
	farmHome(t, srv.URL)
	out, stderr := stubSampleWorker(t, srv.Server)
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), out.String())
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.outcomes) != 1 || srv.outcomes[0]["outcome"] != "UNSUPPORTED_ENVIRONMENT" ||
		!strings.Contains(fmt.Sprint(srv.outcomes[0]["detail"]), have) || !strings.Contains(fmt.Sprint(srv.outcomes[0]["detail"]), "0.0.1") {
		t.Fatalf("outcomes = %+v", srv.outcomes)
	}
	if len(srv.batches) != 0 {
		t.Fatalf("evidence was recorded for a coordinate the host cannot provide: %+v", srv.batches)
	}
	if !strings.Contains(out.String(), `"status":"UNSUPPORTED"`) {
		t.Fatalf("stdout = %s", out.String())
	}
}

// A tool the host does not have is the same measurement.
func TestSampleWorkerCLIRunReportsAMissingToolAsUnsupported(t *testing.T) {
	srv := newCLIRunServer(t, cliAssignment("pkg:generic/cli/opentofu", "opentofu", "", "", runtime.GOOS))
	farmHome(t, srv.URL)
	if _, err := exec.LookPath("tofu"); err == nil {
		t.Skip("tofu is installed here")
	}
	out, stderr := stubSampleWorker(t, srv.Server)
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), out.String())
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.outcomes) != 1 || srv.outcomes[0]["outcome"] != "UNSUPPORTED_ENVIRONMENT" {
		t.Fatalf("outcomes = %+v", srv.outcomes)
	}
}

// A command pattern with a placeholder needs a concrete command line, and
// the one supplied has to canonicalize to the assigned pattern -- otherwise
// the evidence would land on a coordinate nobody asked for.
func TestSampleWorkerCLIRunRequiresArgvThatMatchesThePattern(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	have := environment.Probe(context.Background(), "git")
	if have == "" {
		t.Skip("git version not measurable")
	}
	srv := newCLIRunServer(t, cliAssignment("pkg:generic/cli/git@"+have, "git", have, "worktree add <path>", runtime.GOOS))
	farmHome(t, srv.URL)
	out, stderr := stubSampleWorker(t, srv.Server)
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 3 {
		t.Fatalf("without argv: exit=%d, want 3 (needs a concrete command); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(out.String(), "NEEDS_ARGV") || !strings.Contains(out.String(), "worktree add <path>") {
		t.Fatalf("stdout = %s", out.String())
	}
	stderr.Reset()
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli", "--", "git", "status"}); code != 2 {
		t.Fatalf("mismatched argv: exit=%d, want 2; stderr=%s", code, stderr.String())
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.batches) != 0 || len(srv.outcomes) != 0 {
		t.Fatalf("a refused run left a trace: batches=%+v outcomes=%+v", srv.batches, srv.outcomes)
	}
}

// Held work that is not CLI is not run.
func TestSampleWorkerCLIRunRefusesPackageWork(t *testing.T) {
	srv := newCLIRunServer(t, `{"status":"ASSIGNED","work":{"package":"pkg:npm/axios@1.12.0","kind":"WANTED","axis":"SAMPLE","leaseExpiresAt":"2026-09-19T12:00:00Z"}}`)
	farmHome(t, srv.URL)
	_, stderr := stubSampleWorker(t, srv.Server)
	if code := sampleWorkerMain(context.Background(), []string{"cli-run", "--server", srv.URL, "--token", "csx_author_v1_cli"}); code != 3 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
}
