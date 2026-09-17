package httpapi

// The CLI lane of the authoring funnel.
//
// A CLI coordinate is (tool, version, command, OS), and the farm fills it by
// running the command on a host of that OS with whatever version of the tool
// the host has -- so the work is offered by host OS, the first job for any
// tool is the probe that learns that version, and a coordinate the farm's
// environment cannot provide is handed back as UNSUPPORTED_ENVIRONMENT, an
// outcome that until now only Sample work could report.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type cliWorkResponse struct {
	Status string `json:"status"`
	Work   struct {
		Package  string `json:"package"`
		Symbol   string `json:"symbol"`
		Kind     string `json:"kind"`
		Axis     string `json:"axis"`
		Tool     string `json:"tool"`
		Command  string `json:"command"`
		TargetOS string `json:"targetOS"`
		Score    int64  `json:"score"`
	} `json:"work"`
}

func pollCLI(t *testing.T, srv, token, envelope string) cliWorkResponse {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/v1/authoring/work/next", bytes.NewBufferString(envelope))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("poll status %d", resp.StatusCode)
	}
	var out cliWorkResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

const (
	cliLinuxEnvelope   = `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"` + minCLIWorkClient + `"}`
	cliWindowsEnvelope = `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["windows"],"clientVersion":"` + minCLIWorkClient + `"}`
	cliLegacyEnvelope  = `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`
)

func farmCLIBatch(tool, version, command, os string) domain.ObservationBatch {
	code := 0
	return domain.ObservationBatch{
		SchemaVersion: 2, Epoch: testNow.Format("2006-01-02"), AnonID: "0123456789abcdef0123456789abcdef",
		ProjectBucket: "0123456789abcdef0123456789abcdef",
		Package:       "pkg:generic/cli/" + tool + "@" + version,
		Symbol:        domain.EncodeCLISymbol("", command, domain.ProvenanceFarm),
		Environment:   domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "generic", OS: os, Arch: "amd64"},
		Stage:         domain.StageProjectProcess, Result: domain.ResultPass, ObservationCount: 1,
		TerminationKind: domain.TerminationExit, ExitCode: &code, ActualToolchain: "farm",
	}
}

// An empty network's first CLI work is the seed probe on the worker's own OS.
func TestCLIProbeIsOfferedOnTheWorkersOwnOS(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-linux", testNow)
	got := pollCLI(t, srv.URL, token, cliLinuxEnvelope)
	if got.Status != "ASSIGNED" || got.Work.Kind != "CLI" || got.Work.Axis != serverstore.AuthoringAxisEvidence ||
		got.Work.Package != "pkg:generic/cli/gh" || got.Work.Tool != "gh" || got.Work.Command != "" ||
		got.Work.TargetOS != "linux" || got.Work.Symbol != "[linux]" {
		t.Fatalf("linux worker was handed %+v", got)
	}

	const token2 = "csx_author_v1_YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI"
	authoringSession(t, store, token2, "cli-windows", testNow)
	got = pollCLI(t, srv.URL, token2, cliWindowsEnvelope)
	if got.Status != "ASSIGNED" || got.Work.Kind != "CLI" || got.Work.TargetOS != "windows" || got.Work.Symbol != "[windows]" {
		t.Fatalf("windows worker was handed %+v", got)
	}
}

// hostOS is where the command will actually run. A Windows host driving a
// Linux Docker daemon verifies Linux samples but runs `git` on Windows, so it
// names its host and is handed Windows CLI work.
func TestHostOSDecidesCLIWorkNotTheContainerOS(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-host", testNow)
	got := pollCLI(t, srv.URL, token, `{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"hostOS":"windows","clientVersion":"`+minCLIWorkClient+`"}`)
	if got.Work.Kind != "CLI" || got.Work.TargetOS != "windows" {
		t.Fatalf("host-OS worker was handed %+v", got)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"hostOS":"plan9","clientVersion":"`+minCLIWorkClient+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown hostOS was accepted: %d", resp.StatusCode)
	}
}

// A worker whose `csx sample-worker next` predates the CLI lane would print
// CLI work as package Evidence work and burn the coordinate's attempts on
// instructions it cannot follow. It is not handed any.
func TestCLIWorkIsWithheldFromWorkersThatCannotRunIt(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-legacy", testNow)
	if got := pollCLI(t, srv.URL, token, cliLegacyEnvelope); got.Status != "NO_WORK" {
		t.Fatalf("legacy worker was handed %+v", got)
	}
}

// Once the farm has observed a coordinate it is no longer a gap: the
// snapshot is re-planned from evidence, and a claim a session still holds is
// rechecked live before it is handed back.
func TestCLIWorkConvergesOnceTheFarmObservedIt(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-fill", testNow)
	first := pollCLI(t, srv.URL, token, cliLinuxEnvelope)
	if first.Work.Package != "pkg:generic/cli/gh" || first.Work.Symbol != "[linux]" {
		t.Fatalf("first work %+v", first)
	}
	// Polling again returns the held probe, not new work.
	if again := pollCLI(t, srv.URL, token, cliLinuxEnvelope); again.Work.Symbol != "[linux]" || again.Work.Package != first.Work.Package {
		t.Fatalf("held claim not returned: %+v", again)
	}
	if accepted, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{farmCLIBatch("gh", "2.76.0", "", "linux")}); err != nil || accepted != 1 {
		t.Fatalf("ingest accepted=%d rejected=%+v err=%v", accepted, rejected, err)
	}
	next := pollCLI(t, srv.URL, token, cliLinuxEnvelope)
	if next.Work.Package == "pkg:generic/cli/gh" && next.Work.Symbol == "[linux]" {
		t.Fatalf("the filled probe was handed out again: %+v", next)
	}
	if next.Work.Kind != "CLI" || next.Work.Package != "pkg:generic/cli/git" {
		t.Fatalf("expected the next seed, got %+v", next)
	}
}

// A CLI worker measuring that its host cannot provide the coordinate -- the
// tool is not installed, or is another version -- reports
// UNSUPPORTED_ENVIRONMENT, which is a measurement of the farm's environment
// and is accepted for CLI work exactly as it is for Sample work. Package
// Evidence work keeps refusing it.
func TestCLIWorkAcceptsUnsupportedEnvironment(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-unsupported", testNow)
	got := pollCLI(t, srv.URL, token, cliLinuxEnvelope)
	if got.Work.Kind != "CLI" {
		t.Fatalf("work %+v", got)
	}
	status, body := reportOutcome(t, srv.URL, token,
		`{"schemaVersion":1,"outcome":"UNSUPPORTED_ENVIRONMENT","detail":"farm host has no gh"}`)
	if status != http.StatusOK || body["status"] != "RELEASED" {
		t.Fatalf("report status = %d body = %v", status, body)
	}
	state, found, err := store.AuthoringAttemptState(t.Context(), "generic", "cli/gh", "", "[linux]")
	if err != nil || !found || state.SessionsMeasuringUnsupported != 1 {
		t.Fatalf("attempt state: found=%v err=%v state=%+v", found, err, state)
	}
	// The same session is not handed the coordinate it measured again.
	if next := pollCLI(t, srv.URL, token, cliLinuxEnvelope); next.Work.Package == "pkg:generic/cli/gh" && next.Work.Symbol == "[linux]" {
		t.Fatalf("handed back the coordinate this writer measured unsupported: %+v", next)
	}
}

// CLI seeds are spare-capacity work. Explicit package asks and observed
// package expansion are handed out first; the probes wait behind them.
func TestCLISeedsDoNotDisplaceRequestedPackageWork(t *testing.T) {
	store := newSnapshotStore(expansionRow("observed-package"))
	srv, _, _ := newTestServer(t, func(d *Deps) { d.Store = store })
	seedWanted(t, store, 2)
	var kinds []string
	for n := 1; n <= 4; n++ {
		token := "csx_author_v1_" + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%032d", n)))
		authoringSession(t, store.Fake, token, fmt.Sprintf("cli-order-%d", n), testNow)
		kinds = append(kinds, pollCLI(t, srv.URL, token, cliLinuxEnvelope).Work.Kind)
	}
	want := []string{"WANTED", "WANTED", "EXPANSION", "CLI"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("handout order %v, want %v", kinds, want)
	}
}

// A command-level gap carries the command and the version the farm has, and
// a repeated field failure on it leads the probes of other tools.
func TestCLICommandGapCarriesTheCommandAndVersion(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	field := farmCLIBatch("git", "2.51.0", "worktree add <path>", "windows")
	field.Symbol = domain.EncodeCLISymbol("", "worktree add <path>", domain.ProvenanceField)
	field.ActualToolchain = ""
	field.Result = domain.ResultFail
	field.TerminationKind, field.ExitCode = "", nil
	field.ErrorFingerprint = "sha256:abababababababababababababababababababababababababababababababab"
	field.ErrorCode = "EXIT_128"
	if accepted, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{
		farmCLIBatch("git", "2.47.2", "", "linux"), field,
	}); err != nil || accepted != 2 {
		t.Fatalf("ingest accepted=%d rejected=%+v err=%v", accepted, rejected, err)
	}
	const token = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"
	authoringSession(t, store, token, "cli-command", testNow)
	got := pollCLI(t, srv.URL, token, cliLinuxEnvelope)
	if got.Work.Package != "pkg:generic/cli/git@2.47.2" || got.Work.Command != "worktree add <path>" ||
		got.Work.TargetOS != "linux" || got.Work.Symbol != "[linux] worktree add <path>" || got.Work.Score < serverstore.CLIFailureWeight {
		t.Fatalf("command gap = %+v", got)
	}
}
