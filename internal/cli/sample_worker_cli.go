package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// The farm's CLI executor.
//
// A CLI assignment is (tool, version, command pattern, OS). The farm fills
// it by running the command on a host of that OS with the version of the
// tool the host has -- and what it records is what actually ran: the
// version is measured on the host before anything else, the observation is
// keyed by that measured version, and a coordinate the host cannot provide
// is handed back as UNSUPPORTED_ENVIRONMENT with both versions named rather
// than run against another version and relabelled.
//
// The observation goes through the same recorder as `csx run`, with farm
// provenance, so it is the same structured evidence a user's machine would
// produce: exit or signal or timeout, sanitized fingerprint, stream
// excerpts, the environment. Farm provenance is written here and nowhere
// else, because this is the one path that holds a farm session.

// cliRunDefaultTimeout bounds one observed command. A probe is instant; a
// command that waits for input on a farm host would otherwise hold the
// slot for the whole lease.
const cliRunDefaultTimeout = 10 * time.Minute

// cliRunNeedsArgvExit is the exit status for a pattern with placeholders
// and no concrete command line: the caller (an agent on the farm) fills
// them in and calls again with `--`.
const cliRunNeedsArgvExit = 3

// printCLIWork renders a CLI assignment for the worker that holds it.
func printCLIWork(w io.Writer, work sampleWorkerWork) {
	tool, command, targetOS := cliWorkCoordinate(work)
	fmt.Fprintf(w, "Assigned CLI coverage work (priority score %d, lease until %s)\n", work.Score, work.LeaseExpiresAt.UTC().Format(time.RFC3339))
	if work.Version == "" {
		fmt.Fprintf(w, "Tool: %s (version unknown: this is the probe)\n", tool)
	} else {
		fmt.Fprintf(w, "Tool: %s %s\n", tool, work.Version)
	}
	fmt.Fprintf(w, "OS: %s\nCommand: %s\nAxis: %s\n", targetOS, cliWorkDisplayCommand(tool, command), work.Axis)
	fmt.Fprintln(w, "Produce CLI Evidence, not a sample: run this coordinate on this host with farm provenance and upload it:")
	if domain.CLIWorkHasPlaceholders(command) {
		fmt.Fprintf(w, "  csx sample-worker cli-run -- %s <the command with every <placeholder> replaced by a concrete, harmless value>\n", tool)
		fmt.Fprintln(w, "  the concrete command must canonicalize to the pattern above, or cli-run refuses it.")
	} else {
		fmt.Fprintln(w, "  csx sample-worker cli-run")
	}
	fmt.Fprintln(w, "cli-run measures this host's version of the tool first and hands the coordinate back as unsupported-environment when the host has no such tool or another version; to hand it back yourself:")
	fmt.Fprintln(w, "  csx sample-worker report --outcome unsupported-environment|transient|infrastructure --detail \"one line\"")
}

// cliWorkCoordinate reads the coordinate the server spelled out, falling
// back to the symbol and package for a server that did not.
func cliWorkCoordinate(work sampleWorkerWork) (tool, command, targetOS string) {
	tool, command, targetOS = work.Tool, work.Command, work.TargetOS
	if tool == "" {
		name := work.Name
		if name == "" {
			if p, err := domain.ParsePURL(work.Package); err == nil {
				name = p.Name
			} else if rest, ok := strings.CutPrefix(work.Package, "pkg:generic/"); ok {
				name = rest
			}
		}
		tool, _ = domain.CLIToolFromTargetName(name)
	}
	if targetOS == "" {
		if os, cmd, ok := domain.DecodeCLIWorkSymbol(work.Symbol); ok {
			targetOS, command = os, cmd
		}
	}
	return tool, command, targetOS
}

// cliWorkDisplayCommand is the command line the coordinate names, with the
// probe shown as the version probe it will run.
func cliWorkDisplayCommand(tool, command string) string {
	if command != "" {
		return tool + " " + command
	}
	if argv, ok := environment.ProbeArgv(tool); ok {
		return strings.Join(argv, " ")
	}
	return tool + " (version probe)"
}

// cliRunSummary is the one structured line cli-run prints last, so a farm
// log can be read by a machine as well as an operator.
type cliRunSummary struct {
	Status   string `json:"status"`
	Tool     string `json:"tool"`
	Version  string `json:"version,omitempty"`
	Command  string `json:"command,omitempty"`
	OS       string `json:"os"`
	Result   string `json:"result,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Uploaded int    `json:"uploaded,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func printCLIRunSummary(summary cliRunSummary) {
	// Placeholders are angle brackets; an operator reads them as written.
	enc := json.NewEncoder(sampleWorkerStdout)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(summary)
}

func sampleWorkerCLIRun(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("sample-worker cli-run", flag.ContinueOnError)
	fs.SetOutput(sampleWorkerStderr)
	server := fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token := fs.String("token", "", "sample-worker session token (or "+sampleWorkerSessionTokenEnv+")")
	timeout := fs.Duration("timeout", cliRunDefaultTimeout, "bound on the observed command")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tok := resolveSampleWorkerToken(*token)
	if tok == "" {
		sampleWorkerUsage()
		return 2
	}
	argv := fs.Args()
	base, err := sampleWorkerServerURL(*server)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker: %v\n", err)
		return 2
	}
	// The host OS is where this command runs, whatever the containers run.
	envelope := sampleWorkerEnvelope(ctx)
	envelope["hostOS"] = runtime.GOOS
	result, code := sampleWorkerRequestWork(ctx, base, tok, envelope)
	if code != 0 {
		return code
	}
	if result.Status == "NO_WORK" {
		fmt.Fprintln(sampleWorkerStdout, "NO_WORK: no CLI coordinate is available for this host.")
		return 0
	}
	if result.Status != "ASSIGNED" || result.Work.Package == "" {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker cli-run: invalid assigned work")
		return 1
	}
	work := result.Work
	if work.Kind != "CLI" {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: this session holds %s work on %s, not a CLI coordinate; finish it through `csx sample-worker next`\n", work.Kind, work.Package)
		return cliRunNeedsArgvExit
	}
	tool, command, targetOS := cliWorkCoordinate(work)
	if tool == "" || targetOS == "" {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: cannot read the CLI coordinate from %s %q\n", work.Package, work.Symbol)
		return 1
	}
	summary := cliRunSummary{Tool: tool, Command: command, OS: runtime.GOOS}
	if !strings.EqualFold(targetOS, runtime.GOOS) {
		// The server offers by host OS, so this is a worker that did not
		// say which host it is. Nothing about the coordinate was measured.
		summary.Status, summary.Detail = "INFRASTRUCTURE", "host is "+runtime.GOOS+"; coordinate is "+targetOS
		return cliRunHandBack(ctx, base, tok, "INFRASTRUCTURE", summary)
	}

	// Measure before running: the version this host has is the version the
	// evidence will be keyed by, and a mismatch is an environment fact.
	have := environment.Probe(ctx, tool)
	if have == "" {
		summary.Status, summary.Detail = "UNSUPPORTED", "farm host cannot run "+tool+": not installed, or its version is not measurable"
		return cliRunHandBack(ctx, base, tok, "UNSUPPORTED_ENVIRONMENT", summary)
	}
	summary.Version = have
	if work.Version != "" && work.Version != have {
		summary.Status, summary.Detail = "UNSUPPORTED", "farm host provides "+tool+" "+have+"; coordinate asks "+work.Version
		return cliRunHandBack(ctx, base, tok, "UNSUPPORTED_ENVIRONMENT", summary)
	}

	// The command line: the probe for a probe, the pattern itself when it
	// has no placeholders, and otherwise the caller's argv, which must
	// canonicalize to the pattern so the evidence lands where it was asked.
	if len(argv) == 0 {
		switch {
		case command == "":
			probe, ok := environment.ProbeArgv(tool)
			if !ok || domain.CommandTool(probe[:1]) != tool {
				summary.Status, summary.Detail = "UNSUPPORTED", "farm has no direct version probe for "+tool
				return cliRunHandBack(ctx, base, tok, "UNSUPPORTED_ENVIRONMENT", summary)
			}
			argv = probe
		case domain.CLIWorkHasPlaceholders(command):
			summary.Status, summary.Detail = "NEEDS_ARGV", "replace each <placeholder> in \""+tool+" "+command+"\" with a concrete, harmless value and run: csx sample-worker cli-run -- "+tool+" ..."
			printCLIRunSummary(summary)
			return cliRunNeedsArgvExit
		default:
			argv = append([]string{tool}, strings.Fields(command)...)
		}
	}
	if err := cliRunArgvMatches(argv, tool, command); err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 2
	}

	// A fresh, empty working directory: nothing on the farm host is the
	// subject, and nothing on it is touched.
	dir, err := os.MkdirTemp("", "csx-cli-run-")
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	if err := config.EnsureHome(home); err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	cfg, err := config.Load(home)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	if cfg.Mode != config.ModeCommunity {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker cli-run: farm evidence requires community mode (csx config set mode community)")
		return 1
	}
	ident, err := identity.LoadOrCreate(home)
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	db, err := localdb.Open(filepath.Join(home, "csx.db"))
	if err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: %v\n", err)
		return 1
	}
	defer db.Close()

	res, _ := evidence.Scan(ctx, dir, nil)
	var profile scanner.CommandProfile
	if res != nil {
		profile = res.Classify(argv)
	}
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	exitCode, output, runErr := evidence.Run(runCtx, argv, dir)
	cancel()
	if output.ToolVersion == "" {
		// Run skips its own probe under a deadline; the version was
		// measured above, on this host, before the command ran.
		output.ToolVersion = have
	}
	if runErr != nil {
		if output.Stderr == "" {
			output.Stderr = runErr.Error()
		}
		exitCode = -1
	}
	rec := &evidence.Recorder{DB: db, Ident: ident, Cfg: cfg, CLIProvenance: domain.ProvenanceFarm}
	if err := rec.RecordCommandOutput(ctx, dir, res, profile, argv, exitCode, output); err != nil {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: record evidence: %v\n", err)
		return 1
	}
	batcher := &evidence.Batcher{DB: db, Ident: ident, Cfg: cfg}
	uctx, ucancel := context.WithTimeout(ctx, 30*time.Second)
	uploaded, uploadErr := batcher.Upload(uctx, &http.Client{Timeout: 20 * time.Second}, cfg.ServerURL)
	ucancel()
	summary.Status = "RECORDED"
	summary.Result = string(domain.ResultPass)
	if exitCode != 0 || output.Termination.Kind != "" {
		summary.Result = string(domain.ResultFail)
	}
	if runErr == nil {
		code := exitCode
		summary.ExitCode = &code
	}
	summary.Uploaded = uploaded
	if uploadErr != nil || uploaded == 0 {
		summary.Detail = "recorded locally; upload pending -- run `csx sync` before the next poll or the coordinate is offered again"
		if uploadErr != nil {
			fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: upload: %v\n", uploadErr)
		}
	}
	printCLIRunSummary(summary)
	return 0
}

// cliRunArgvMatches refuses a command line that would record a coordinate
// other than the one assigned: another tool, or a command that does not
// canonicalize to the assigned pattern.
func cliRunArgvMatches(argv []string, tool, command string) error {
	if len(argv) == 0 {
		return errors.New("empty command")
	}
	if got := domain.CommandTool(argv); got != tool {
		return fmt.Errorf("command runs %q, the coordinate is %q", got, tool)
	}
	if command == "" {
		probe, ok := environment.ProbeArgv(tool)
		if !ok || strings.Join(argv, " ") != strings.Join(probe, " ") {
			return fmt.Errorf("a probe runs exactly %q", strings.Join(probe, " "))
		}
		return nil
	}
	parsed := domain.ParseCLICommand(argv, domain.EnvironmentFingerprint{})
	got := strings.TrimSpace(parsed.Subcommand + " " + parsed.ArgsPattern)
	if got != command {
		return fmt.Errorf("command canonicalizes to %q, the coordinate is %q", got, command)
	}
	return nil
}

// cliRunHandBack reports an outcome for the held coordinate and prints the
// summary. The exit status is 0: a measured environment fact is a finished
// job, and the farm's loop should ask for the next one.
func cliRunHandBack(ctx context.Context, base, tok, wire string, summary cliRunSummary) int {
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "outcome": wire, "detail": summary.Detail})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/authoring/work/outcome", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker cli-run: invalid outcome request")
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := sampleWorkerClient.Do(req)
	if err != nil {
		fmt.Fprintln(sampleWorkerStderr, "csx sample-worker cli-run: outcome report failed")
		return 1
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, sampleWorkerResponseLimit+1))
	if readErr != nil || len(body) > sampleWorkerResponseLimit || resp.StatusCode != http.StatusOK {
		fmt.Fprintf(sampleWorkerStderr, "csx sample-worker cli-run: server rejected the %s report (HTTP %d)\n", wire, resp.StatusCode)
		return 1
	}
	printCLIRunSummary(summary)
	return 0
}
