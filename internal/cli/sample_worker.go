package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sandbox"
)

const sampleWorkerResponseLimit = 32 << 10

var (
	sampleWorkerStdout io.Writer = os.Stdout
	sampleWorkerStderr io.Writer = os.Stderr
	sampleWorkerClient           = &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	sampleWorkerCapability = sandbox.Detect
)

func init() {
	Register(Command{
		Name:    "sample-worker",
		Summary: "refresh a sample-authoring session, take or hand back work, or submit a private draft",
		Run:     sampleWorkerMain,
	})
}

func sampleWorkerMain(ctx context.Context, args []string) int {
	if len(args) == 0 {
		sampleWorkerUsage()
		return 2
	}
	if args[0] == "submit" {
		return sampleWorkerSubmit(ctx, args[1:])
	}
	if args[0] == "next" {
		return sampleWorkerNext(ctx, args[1:])
	}
	if args[0] == "report" {
		return sampleWorkerReport(ctx, args[1:])
	}
	if args[0] != "refresh" {
		sampleWorkerUsage()
		return 2
	}
	fs := flag.NewFlagSet("sample-worker refresh", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token (or "+sampleWorkerSessionTokenEnv+")")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	tok := resolveSampleWorkerToken(*token)
	if fs.NArg() != 0 || tok == "" {
		fmt.Fprintln(sampleWorkerStderr, "usage: csx sample-worker refresh --server URL --token TOKEN")
		return 2
	}
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	computerName, _ := os.Hostname()
	computerName = strings.TrimSpace(computerName)
	if len(computerName) > 120 {
		computerName = computerName[:120]
	}
	payload, _ := json.Marshal(map[string]string{"computerName": computerName})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/session/refresh", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh request")
		return 2
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: refresh failed; stop starting new sample work")
		return 1
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if err != nil || len(body) > sampleWorkerResponseLimit {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh response; stop starting new sample work")
		return 1
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: session unavailable (HTTP %d); stop starting new sample work\n", resp.StatusCode)
		return 1
	}
	var result struct {
		IdleExpiresAt time.Time `json:"idleExpiresAt"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.IdleExpiresAt.IsZero() {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker: invalid refresh response; stop starting new sample work")
		return 1
	}
	fmt.Fprintf(sampleWorkerStdout, "sample-worker session active until %s\n", result.IdleExpiresAt.UTC().Format(time.RFC3339))
	return 0
}

func sampleWorkerUsage() {
	fmt.Fprintln(sampleWorkerStderr, "usage: csx sample-worker refresh --server URL --token TOKEN")
	fmt.Fprintln(sampleWorkerStderr, "       csx sample-worker next --server URL --token TOKEN")
	fmt.Fprintln(sampleWorkerStderr, "       csx sample-worker submit <sampleId> --server URL --token TOKEN")
	fmt.Fprintln(sampleWorkerStderr, "       csx sample-worker report --outcome KIND [--detail TEXT] --server URL --token TOKEN")
	fmt.Fprintln(sampleWorkerStderr, "         KIND: no-callable-symbol | transient | infrastructure | no-output")
	fmt.Fprintln(sampleWorkerStderr, "  the token may be supplied in "+sampleWorkerSessionTokenEnv+" instead of --token")
}

// sampleWorkerSessionTokenEnv is where a worker script hands the session
// bearer to csx without ever placing it on a command line. /proc/<pid>/cmdline
// is world-readable and /proc/<pid>/environ is not, so a token carried in the
// environment is not visible to another local account the way a --token
// argument is (CodeSampleX-Farm#14). The generated worker scripts export it;
// --token stays accepted so anyone running these commands by hand is unaffected.
const sampleWorkerSessionTokenEnv = "CSX_SESSION_TOKEN"

// resolveSampleWorkerToken prefers an explicit --token but falls back to the
// environment, so the token never has to appear in argv.
func resolveSampleWorkerToken(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv(sampleWorkerSessionTokenEnv))
}

// sampleWorkerOutcomes maps what a writer types to what the protocol carries.
//
// The classification cannot come from the server: it can observe that it gave
// a coordinate out and got nothing back, and nothing else. Telling a Docker
// daemon that died from a registry that was down from an artifact that
// contains no code is the writer's job, and it is the whole difference between
// a bad afternoon and a permanent exclusion.
var sampleWorkerOutcomes = map[string]string{
	// The strong one: you looked, and no symbol or project a contract could
	// call exists here — a pom with no jar, a plugin marker, a lone .node
	// binary the parent package selects internally.
	"no-callable-symbol": "NO_CALLABLE_SYMBOL",
	// A registry or a toolchain that would not answer. It has not said no.
	"transient": "TRANSIENT",
	// Your own machine: no Docker daemon, no disk, no route. This measures
	// nothing about the coordinate and is not counted against it.
	"infrastructure": "INFRASTRUCTURE",
	// You gave up and cannot say which of the above it was.
	"no-output": "NO_OUTPUT",
}

func sampleWorkerOutcomeNames() string {
	names := make([]string, 0, len(sampleWorkerOutcomes))
	for name := range sampleWorkerOutcomes {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, " | ")
}

// sampleWorkerReport hands the claim back with a classification.
//
// It exists because a writer holding a coordinate it cannot author had exactly
// one way out: stop asking. The claim then sat on a 24-hour lease while the
// session stayed alive, and the coordinate was off the board for everybody —
// which is how one Gradle plugin marker took an authoring slot for four hours.
func sampleWorkerReport(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("sample-worker report", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token")
	outcome := fs.String("outcome", "", "why the work produced nothing: "+sampleWorkerOutcomeNames())
	detail := fs.String("detail", "", "one line an operator will read, e.g. \"pom-only artifact: no jar\"")
	if err := fs.Parse(args); err != nil {
		sampleWorkerUsage()
		return 2
	}
	tok := resolveSampleWorkerToken(*token)
	if fs.NArg() != 0 || tok == "" {
		sampleWorkerUsage()
		return 2
	}
	wire, known := sampleWorkerOutcomes[strings.ToLower(strings.TrimSpace(*outcome))]
	if !known {
		// Refused here rather than sent. An unrecognised classification is
		// worse than none: it would be recorded as evidence about the
		// coordinate without anybody having measured anything.
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker report: --outcome must be one of %s\n", sampleWorkerOutcomeNames())
		return 2
	}
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	payload, _ := json.Marshal(map[string]any{
		"schemaVersion": 1, "outcome": wire, "detail": strings.TrimSpace(*detail),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/work/outcome", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker report: invalid request")
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker report: request failed")
		return 1
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if readErr != nil || len(body) > sampleWorkerResponseLimit || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker report: server rejected the report (HTTP %d)\n", resp.StatusCode)
		return 1
	}
	var result struct {
		Status string `json:"status"`
		Work   struct {
			Package string `json:"package"`
			Symbol  string `json:"symbol"`
		} `json:"work"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker report: invalid server response")
		return 1
	}
	if result.Status == "NO_CLAIM" {
		fmt.Fprintln(sampleWorkerStdout, "NO_CLAIM: this session was not holding any work; nothing was recorded.")
		return 0
	}
	target := result.Work.Package
	if result.Work.Symbol != "" {
		target += " · " + result.Work.Symbol
	}
	fmt.Fprintf(sampleWorkerStdout, "Released %s as %s. Ask for work again with `csx sample-worker next`.\n", target, wire)
	return 0
}

// sampleWorkerContainerOS asks the Docker daemon which kind of container it
// serves. A variable so a test can state the answer.
var sampleWorkerContainerOS = sandbox.DetectContainerOS

// sampleWorkerEnvelope describes this machine to the work endpoint.
//
// verifierOS was the literal []string{"linux"}. It happened to be true while
// every worker ran Linux containers, and it would have stayed true-looking
// after somebody switched their daemon to Windows containers — the work would
// have been claimed as linux and the receipt stamped windows. A worker has to
// report what it can actually execute, not what workers used to execute.
func sampleWorkerEnvelope(ctx context.Context) map[string]any {
	return map[string]any{
		"schemaVersion":     1,
		"sandboxCapability": sampleWorkerCapability(ctx),
		"verifierOS":        []string{sampleWorkerContainerOS(ctx)},
		// The server refuses a worker that cannot say which release it is,
		// because a worker it cannot identify is exactly the one it must not
		// trust to describe its own environment.
		"clientVersion": Version,
	}
}

func sampleWorkerNext(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("sample-worker next", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tok := resolveSampleWorkerToken(*token)
	if fs.NArg() != 0 || tok == "" {
		return 2
	}
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	payload, _ := json.Marshal(sampleWorkerEnvelope(ctx))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/work/next", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker next: invalid request")
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker next: work request failed")
		return 1
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if readErr != nil || len(body) > sampleWorkerResponseLimit || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker next: server rejected work request (HTTP %d)\n", resp.StatusCode)
		return 1
	}
	var result struct {
		Status string `json:"status"`
		Work   struct {
			Package        string    `json:"package"`
			Symbol         string    `json:"symbol"`
			Asks           int64     `json:"asks"`
			Kind           string    `json:"kind"`
			Score          int64     `json:"score"`
			LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
		} `json:"work"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker next: invalid server response")
		return 1
	}
	if result.Status == "NO_WORK" {
		fmt.Fprintln(sampleWorkerStdout, "NO_WORK: no uncovered Wanted or evidence-driven expansion work is available for this worker.")
		return 0
	}
	if result.Status != "ASSIGNED" || result.Work.Package == "" || result.Work.LeaseExpiresAt.IsZero() {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker next: invalid assigned work")
		return 1
	}
	goal := "verify " + result.Work.Package
	if result.Work.Symbol != "" {
		goal = "verify " + result.Work.Symbol + " in " + result.Work.Package
	}
	if result.Work.Kind == "FINDING" {
		fmt.Fprintf(sampleWorkerStdout, "Assigned Finding-miner work (evidence score %d, lease until %s)\n", result.Work.Score, result.Work.LeaseExpiresAt.UTC().Format(time.RFC3339))
	} else if result.Work.Kind == "EXPANSION" {
		fmt.Fprintf(sampleWorkerStdout, "Assigned coverage-expansion work (evidence score %d, lease until %s)\n", result.Work.Score, result.Work.LeaseExpiresAt.UTC().Format(time.RFC3339))
	} else if result.Work.Kind == "DEPENDENCY" {
		// Named apart from expansion on purpose. The score means something
		// different here -- it counts the projects whose lockfile resolved
		// onto this exact release, not observations of anybody using it -- and
		// nobody has reported using this coordinate directly at all, so a
		// writer should expect less prior art than usual and not read that as
		// a sign the coordinate is wrong.
		fmt.Fprintf(sampleWorkerStdout, "Assigned dependency-closure work (%d projects resolved it, lease until %s)\n", result.Work.Score, result.Work.LeaseExpiresAt.UTC().Format(time.RFC3339))
	} else {
		fmt.Fprintf(sampleWorkerStdout, "Assigned Wanted work (%d asks, lease until %s)\n", result.Work.Asks, result.Work.LeaseExpiresAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(sampleWorkerStdout, "Package: %s\nSymbol: %s\n", result.Work.Package, result.Work.Symbol)
	fmt.Fprintf(sampleWorkerStdout, "Start exactly here:\n  csx sample propose --goal %q --package %q", goal, result.Work.Package)
	if result.Work.Symbol != "" {
		fmt.Fprintf(sampleWorkerStdout, " --symbol %q", result.Work.Symbol)
	}
	fmt.Fprintln(sampleWorkerStdout)
	// The way out, printed beside the way in. A writer that cannot author this
	// coordinate had exactly one option before this line existed — stop asking
	// — and the claim then held the slot for the rest of its 24-hour lease.
	fmt.Fprintln(sampleWorkerStdout,
		"If nothing here can be written against, hand it back with a reason instead of asking again:\n"+
			"  csx sample-worker report --outcome no-callable-symbol|transient|infrastructure --detail \"one line\"")
	return 0
}

func sampleWorkerSubmit(ctx context.Context, args []string) int {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		sampleWorkerUsage()
		return 2
	}
	sampleID := args[0]
	fs := flag.NewFlagSet("sample-worker submit", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	tok := resolveSampleWorkerToken(*token)
	if fs.NArg() != 0 || tok == "" {
		return 2
	}
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	env, err := openSampleEnv()
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	defer env.Close()
	row, err := resolveLocalSample(ctx, env.db, sampleID)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	if row.Status != "LOCAL" && row.Status != "LOCAL_PASS" {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: status %s is not a local draft\n", row.Status)
		return 1
	}
	artifact, err := readSampleArtifact(env.cas, row.SampleID)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	dir, err := unpackToTemp(artifact)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	findings, err := samples.Scan(dir, publishProvenance())
	if err != nil || len(findings) != 0 {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: refused; leakage findings=%d\n", len(findings))
		return 1
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"manifest": row.ManifestJSON, "sampleId": row.SampleID, "localStatus": row.Status,
	} {
		if err := mw.WriteField(name, value); err != nil {
			fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
			return 1
		}
	}
	fw, err := mw.CreateFormFile("artifact", "sample.tar.gz")
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	if _, err := fw.Write(artifact); err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	if err := mw.Close(); err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: %v\n", err)
		return 1
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/drafts", &body)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker submit: invalid request")
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker submit: upload failed")
		return 1
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker submit: server rejected draft (HTTP %d)\n", resp.StatusCode)
		return 1
	}
	var result struct {
		SampleID string `json:"sampleId"`
		Status   string `json:"status"`
	}
	wantStatus := "PRIVATE_DRAFT"
	if row.Status == "LOCAL_PASS" {
		wantStatus = "CROSS_PENDING"
	}
	decodeErr := json.Unmarshal(responseBody, &result)
	validStatus := result.Status == wantStatus || (row.Status == "LOCAL_PASS" &&
		(result.Status == "CROSS_PASS" || result.Status == "MATRIX_PASS" || result.Status == "STABLE"))
	if len(responseBody) > sampleWorkerResponseLimit || decodeErr != nil || result.SampleID != row.SampleID || !validStatus {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker submit: invalid server response")
		return 1
	}
	fmt.Fprintf(sampleWorkerStdout, "Private sample draft submitted: %s (%s, %s)\n", row.SampleID, row.Status, result.Status)
	return 0
}

func sampleWorkerServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("server must be an origin URL")
	}
	host := strings.ToLower(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "127.0.0.1" || host == "localhost" || host == "::1")) {
		return "", fmt.Errorf("server must use HTTPS")
	}
	return strings.TrimRight(u.String(), "/"), nil
}
