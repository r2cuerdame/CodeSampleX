package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
)

// csx fix-claims is the client side of the fix-claim verification lane
// (#444). The collector runs on any machine with the GitHub API in reach;
// submit, next, runs and report run on the Farm under the same session
// token csx sample-worker uses, so a fix worker is a sample worker with a
// different queue.

const fixClaimsResponseLimit = 256 << 10

var (
	fixClaimsStdout io.Writer = os.Stdout
	fixClaimsStderr io.Writer = os.Stderr
	fixClaimsStdin  io.Reader = os.Stdin
	fixClaimsClient           = sampleWorkerClient
	// fixClaimsCollector is the release reader; tests point it at a local
	// server.
	fixClaimsCollector = &fixclaims.Collector{}
)

func init() {
	Register(Command{
		Name:    "fix-claims",
		Summary: "collect upstream bug-fix claims, submit candidates, take fix-verification work, and report runs",
		Run:     fixClaimsMain,
	})
}

func fixClaimsUsage() {
	w := fixClaimsStderr
	fmt.Fprintln(w, "usage: csx fix-claims collect --repo OWNER/NAME --ecosystem ECO --package NAME [--releases N] [--limit N] [--out FILE]")
	fmt.Fprintln(w, "       csx fix-claims collect --seeds FILE [--releases N] [--limit N] [--out FILE]")
	fmt.Fprintln(w, "       csx fix-claims submit [FILE] --server URL --token TOKEN")
	fmt.Fprintln(w, "       csx fix-claims next [--os linux] --server URL --token TOKEN")
	fmt.Fprintln(w, "       csx fix-claims work --repro-root DIR [--os linux] [--once | --max N] --server URL --token TOKEN")
	fmt.Fprintln(w, "         take leases and run each probe from DIR/<name>/{fix-claim.json,csx.json,...}; needs Docker")
	fmt.Fprintln(w, "       csx fix-claims probe --repro DIR --version V [--os linux] [--runtime R --runtime-version RV] --server URL --token TOKEN")
	fmt.Fprintln(w, "       csx fix-claims runs --id ID [FILE] --server URL --token TOKEN")
	fmt.Fprintln(w, "       csx fix-claims report --id ID --outcome KIND [--detail TEXT] --server URL --token TOKEN")
	fmt.Fprintln(w, "         KIND: no-reproducer | infrastructure | transient | no-output")
	fmt.Fprintln(w, "       csx fix-claims prompt [FILE]        render the AGY extraction prompt for one input document")
	fmt.Fprintln(w, "       csx fix-claims metrics --server URL --token TOKEN")
	fmt.Fprintln(w, "  the token may be supplied in "+sampleWorkerSessionTokenEnv+" instead of --token;")
	fmt.Fprintln(w, "  collect reads an optional GitHub token from CSX_GITHUB_TOKEN. FILE defaults to stdin.")
}

func fixClaimsMain(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fixClaimsUsage()
		return 2
	}
	switch args[0] {
	case "collect":
		return fixClaimsCollect(ctx, args[1:])
	case "submit":
		return fixClaimsSubmit(ctx, args[1:])
	case "next":
		return fixClaimsNext(ctx, args[1:])
	case "work":
		return fixClaimsWork(ctx, args[1:])
	case "probe":
		return fixClaimsProbe(ctx, args[1:])
	case "runs":
		return fixClaimsRuns(ctx, args[1:])
	case "report":
		return fixClaimsReport(ctx, args[1:])
	case "prompt":
		return fixClaimsPrompt(args[1:])
	case "metrics":
		return fixClaimsMetrics(ctx, args[1:])
	}
	fixClaimsUsage()
	return 2
}

// fixClaimsSeed is one line of a --seeds file: a package and the GitHub
// repository that publishes its releases.
type fixClaimsSeed = fixclaims.Source

func fixClaimsCollect(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims collect", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	repo := fs.String("repo", "", "GitHub repository, owner/name")
	ecosystem := fs.String("ecosystem", "", "package ecosystem (npm, pypi, cargo, golang, maven, gem, hex, pub)")
	name := fs.String("package", "", "package name")
	seeds := fs.String("seeds", "", "JSON file listing sources: [{\"ecosystem\",\"name\",\"repo\"}]")
	releases := fs.Int("releases", 5, "stable releases to read per source")
	limit := fs.Int("limit", 0, "keep at most N candidates, highest confidence first (0 = all)")
	out := fs.String("out", "", "write the candidate submission to FILE instead of stdout")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	var sources []fixClaimsSeed
	switch {
	case *seeds != "":
		raw, err := os.ReadFile(*seeds)
		if err != nil {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims collect: %v\n", err)
			return 2
		}
		var file struct {
			Sources []fixClaimsSeed `json:"sources"`
		}
		if err := json.Unmarshal(raw, &file); err != nil || len(file.Sources) == 0 {
			// A bare list is accepted too.
			if err := json.Unmarshal(raw, &sources); err != nil || len(sources) == 0 {
				fmt.Fprintln(fixClaimsStderr, "csx fix-claims collect: seeds must be a JSON list of {ecosystem, name, repo}")
				return 2
			}
		} else {
			sources = file.Sources
		}
	case *repo != "" && *ecosystem != "" && *name != "":
		sources = []fixClaimsSeed{{Ecosystem: *ecosystem, Name: *name, Repo: *repo}}
	default:
		fixClaimsUsage()
		return 2
	}
	collector := fixClaimsCollector
	if collector.Token == "" {
		collector.Token = strings.TrimSpace(os.Getenv("CSX_GITHUB_TOKEN"))
	}
	var candidates []fixclaims.Candidate
	var collections []fixclaims.Collection
	failed := 0
	for _, src := range sources {
		col, err := collector.Collect(ctx, src, *releases)
		if err != nil {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims collect: %s: %v\n", src.Repo, err)
			failed++
			continue
		}
		collections = append(collections, col)
		candidates = append(candidates, col.Candidates...)
		fmt.Fprintf(fixClaimsStderr, "%s/%s: %d releases, %d candidates, %d lines skipped\n", src.Ecosystem, src.Name, col.Releases, len(col.Candidates), len(col.Skipped))
	}
	candidates = fixclaims.Limit(candidates, *limit)
	if candidates == nil {
		candidates = []fixclaims.Candidate{}
	}
	doc := map[string]any{"schemaVersion": 1, "candidates": candidates, "collections": collections}
	encoded, _ := json.MarshalIndent(doc, "", "  ")
	if *out != "" {
		if err := os.WriteFile(*out, append(encoded, '\n'), 0o644); err != nil {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims collect: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintln(fixClaimsStdout, string(encoded))
	}
	if failed == len(sources) {
		return 1
	}
	return 0
}

// fixClaimsSplitPositional separates a file argument from the flags
// wherever it appears (csx fix-claims runs --id 7 FILE --server URL), which
// is how the other worker commands read. Every flag here takes a value, so
// a flag token consumes the next token unless it carries "=".
func fixClaimsSplitPositional(args []string) (positional, flags []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return positional, flags
}

func fixClaimsReadDocument(args []string) ([]byte, bool) {
	var raw []byte
	var err error
	if len(args) > 0 && args[0] != "-" {
		raw, err = os.ReadFile(args[0])
	} else {
		raw, err = io.ReadAll(io.LimitReader(fixClaimsStdin, 4<<20))
	}
	if err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims: %v\n", err)
		return nil, false
	}
	return raw, true
}

// fixClaimsCall posts (or gets) one JSON document to the server under the
// session bearer and returns the decoded answer.
func fixClaimsCall(ctx context.Context, method, base, tok, path string, payload []byte) (int, map[string]any, bool) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims: invalid request")
		return 0, nil, false
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := fixClaimsClient.Do(req)
	if err != nil {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims: request failed")
		return 0, nil, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, fixClaimsResponseLimit+1))
	if err != nil || len(raw) > fixClaimsResponseLimit {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims: invalid server response (HTTP %d)\n", resp.StatusCode)
		return resp.StatusCode, nil, false
	}
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims: invalid server response (HTTP %d)\n", resp.StatusCode)
			return resp.StatusCode, nil, false
		}
	}
	return resp.StatusCode, decoded, true
}

func fixClaimsServerFlags(fs *flag.FlagSet) (server, token *string) {
	server = fs.String("server", "https://codesamplex.dev", "CodeSampleX server URL")
	token = fs.String("token", "", "writer session token (or "+sampleWorkerSessionTokenEnv+")")
	return
}

func fixClaimsResolve(server, token string) (string, string, bool) {
	tok := resolveSampleWorkerToken(token)
	if tok == "" {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims: a session token is required")
		return "", "", false
	}
	base, err := sampleWorkerServerURL(server)
	if err != nil {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims: %v\n", err)
		return "", "", false
	}
	return base, tok, true
}

func fixClaimsPrintJSON(v any) {
	encoded, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(fixClaimsStdout, string(encoded))
}

func fixClaimsSubmit(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims submit", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	positional, flags := fixClaimsSplitPositional(args)
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	raw, ok := fixClaimsReadDocument(append(positional, fs.Args()...))
	if !ok {
		return 2
	}
	// Validate locally first so a producer sees every rejection before a
	// request is spent, and only the candidate list travels.
	var doc struct {
		SchemaVersion int                   `json:"schemaVersion"`
		Candidates    []fixclaims.Candidate `json:"candidates"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Candidates) == 0 {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims submit: the document must hold {schemaVersion:1, candidates:[...]}")
		return 2
	}
	var valid []fixclaims.Candidate
	for i, c := range doc.Candidates {
		normalized, rejections := fixclaims.Validate(c)
		if len(rejections) > 0 {
			codes := make([]string, 0, len(rejections))
			for _, r := range rejections {
				codes = append(codes, r.Code)
			}
			fmt.Fprintf(fixClaimsStderr, "candidate %d refused locally: %s\n", i, strings.Join(codes, ", "))
			continue
		}
		valid = append(valid, normalized)
	}
	if len(valid) == 0 {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims submit: nothing valid to submit")
		return 1
	}
	accepted, rejected := 0, 0
	for start := 0; start < len(valid); start += 50 {
		end := start + 50
		if end > len(valid) {
			end = len(valid)
		}
		payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "candidates": valid[start:end]})
		code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, "/v1/fix-claims/candidates", payload)
		if !ok || code != http.StatusOK {
			fmt.Fprintf(fixClaimsStderr, "csx fix-claims submit: server refused the batch (HTTP %d): %v\n", code, body["error"])
			return 1
		}
		if list, _ := body["accepted"].([]any); list != nil {
			accepted += len(list)
		}
		if list, _ := body["rejected"].([]any); list != nil {
			rejected += len(list)
			for _, r := range list {
				fmt.Fprintf(fixClaimsStderr, "server rejected: %v\n", r)
			}
		}
	}
	fmt.Fprintf(fixClaimsStdout, "submitted %d candidates: %d accepted, %d rejected\n", len(valid), accepted, rejected)
	return 0
}

func fixClaimsNext(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims next", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	osFlag := fs.String("os", "", "comma-separated operating systems this worker can run (default: the verifier's own)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	envelope := map[string]any{"schemaVersion": 1}
	if *osFlag != "" {
		var oses []string
		for _, o := range strings.Split(*osFlag, ",") {
			if o = strings.TrimSpace(strings.ToLower(o)); o != "" {
				oses = append(oses, o)
			}
		}
		envelope["verifierOS"] = oses
	} else if verifierOS := sampleWorkerEnvelope(ctx)["verifierOS"]; verifierOS != nil {
		envelope["verifierOS"] = verifierOS
	}
	payload, _ := json.Marshal(envelope)
	code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, "/v1/fix-claims/work/next", payload)
	if !ok || code != http.StatusOK {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims next: server rejected the poll (HTTP %d): %v\n", code, body["error"])
		return 1
	}
	fixClaimsPrintJSON(body)
	return 0
}

func fixClaimsRuns(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims runs", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	id := fs.Int64("id", 0, "fix candidate id from `csx fix-claims next`")
	positional, flags := fixClaimsSplitPositional(args)
	if err := fs.Parse(flags); err != nil {
		return 2
	}
	if *id <= 0 {
		fixClaimsUsage()
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	raw, ok := fixClaimsReadDocument(append(positional, fs.Args()...))
	if !ok {
		return 2
	}
	var doc struct {
		SchemaVersion int             `json:"schemaVersion"`
		Runs          []fixclaims.Run `json:"runs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Runs) == 0 {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims runs: the document must hold {schemaVersion:1, runs:[...]}")
		return 2
	}
	doc.SchemaVersion = 1
	payload, _ := json.Marshal(doc)
	code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, fmt.Sprintf("/v1/fix-claims/%d/runs", *id), payload)
	if !ok || code != http.StatusOK {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims runs: server refused the runs (HTTP %d): %v\n", code, body["error"])
		return 1
	}
	fixClaimsPrintJSON(body)
	return 0
}

func fixClaimsReport(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims report", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	id := fs.Int64("id", 0, "fix candidate id")
	outcome := fs.String("outcome", "", "no-reproducer | infrastructure | transient | no-output")
	detail := fs.String("detail", "", "one line for the operator")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	kind := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(*outcome), "-", "_"))
	if *id <= 0 || kind == "" {
		fixClaimsUsage()
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "outcome": kind, "detail": *detail})
	code, body, ok := fixClaimsCall(ctx, http.MethodPost, base, tok, fmt.Sprintf("/v1/fix-claims/%d/outcome", *id), payload)
	if !ok || code != http.StatusOK {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims report: server refused the report (HTTP %d): %v\n", code, body["error"])
		return 1
	}
	fixClaimsPrintJSON(body)
	return 0
}

func fixClaimsPrompt(args []string) int {
	raw, ok := fixClaimsReadDocument(args)
	if !ok {
		return 2
	}
	var in fixclaims.AGYInput
	if err := json.Unmarshal(raw, &in); err != nil || in.Source.Name == "" || in.Line == "" {
		fmt.Fprintln(fixClaimsStderr, "csx fix-claims prompt: the input must hold source{ecosystem,name,repo}, release, releaseUrl and line")
		return 2
	}
	fmt.Fprint(fixClaimsStdout, fixclaims.AGYPrompt(in))
	return 0
}

func fixClaimsMetrics(ctx context.Context, args []string) int {
	fs := flag.NewFlagSet("fix-claims metrics", flag.ContinueOnError)
	fs.SetOutput(fixClaimsStderr)
	server, token := fixClaimsServerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	base, tok, ok := fixClaimsResolve(*server, *token)
	if !ok {
		return 2
	}
	code, body, ok := fixClaimsCall(ctx, http.MethodGet, base, tok, "/v1/fix-claims/metrics", nil)
	if !ok || code != http.StatusOK {
		fmt.Fprintf(fixClaimsStderr, "csx fix-claims metrics: HTTP %d: %v\n", code, body["error"])
		return 1
	}
	fixClaimsPrintJSON(body)
	return 0
}
