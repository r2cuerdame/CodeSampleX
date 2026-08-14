package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
)

func init() {
	Register(Command{
		Name:    "search",
		Summary: "search verified samples: csx search <query...> [--json] [--package purl]...",
		Run:     searchMain,
	})
}

// searchMain implements `csx search <query...> [--json] [--package p]...`.
// The environment comes from a best-effort scan of the current directory;
// the query runs against the daemon when one is up, else directly against
// the local engine — search never needs the server (goal.md §3.9).
func searchMain(ctx context.Context, args []string) int {
	var words, pkgs []string
	jsonOut := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			jsonOut = true
		case a == "--package":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "csx: --package requires a purl argument")
				return 2
			}
			pkgs = append(pkgs, args[i])
		case strings.HasPrefix(a, "--package="):
			pkgs = append(pkgs, strings.TrimPrefix(a, "--package="))
		default:
			words = append(words, a)
		}
	}
	query := strings.Join(words, " ")
	if query == "" && len(pkgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: csx search <query...> [--json] [--package purl]...")
		return 2
	}

	// Best-effort environment AND dependency set from the current project
	// (no publicness checks here — nothing is uploaded by a search).
	// Auto-completing packages from the lockfile is what makes an in-project
	// search environment-aware without the caller listing purls (§11.1).
	env := domain.EnvironmentFingerprint{SchemaVersion: 1}
	var symbols []string
	if dir, err := os.Getwd(); err == nil {
		if res, err := evidence.Scan(ctx, dir, nil); err == nil && res != nil {
			env = res.Env
			if len(pkgs) == 0 {
				pkgs = projectPackages(res)
			}
			symbols = projectSymbols(res)
		} else {
			env = environment.Collect(ctx, nil)
		}
	}

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         query,
		Packages:      pkgs,
		Symbols:       symbols,
		Environment:   env,
	}

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	resp, err := searchViaDaemon(ctx, home, req)
	if err != nil {
		// Daemon down: query the local engine directly.
		d, derr := daemon.New(home)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "csx: search: %v\n", derr)
			return 1
		}
		defer d.Close()
		// SearchAndRecord, not Engine.Search: with the daemon down this path
		// counted nothing, so csx stats read 0 for every search made while
		// it was not running.
		r := d.SearchAndRecord(ctx, req)
		resp = &r
	}

	if jsonOut {
		out, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}
	renderSearchText(os.Stdout, *resp)
	return 0
}

// maxAutoPackages bounds how many project dependencies a search carries;
// beyond this the query stops being about the packages in play.
const maxAutoPackages = 24

// projectPackages returns the scanned public-registry dependencies as purls,
// direct dependencies first. Private and unknown-publicness packages are
// included here (nothing is uploaded by a search) but a private purl can
// never match a public sample, so they only cost a slot — hence direct-first.
func projectPackages(res *scanner.ScanResult) []string {
	var direct, indirect []string
	for _, p := range res.Packages {
		if p.Publicness == scanner.PublicnessPrivate || p.PURL.Version == "" {
			continue
		}
		if p.Direct {
			direct = append(direct, p.PURL.String())
		} else {
			indirect = append(indirect, p.PURL.String())
		}
	}
	out := append(direct, indirect...)
	if len(out) > maxAutoPackages {
		out = out[:maxAutoPackages]
	}
	return out
}

// projectSymbols returns distinct public symbol families observed in the
// project, capped like packages.
func projectSymbols(res *scanner.ScanResult) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range res.Symbols {
		if s.Family == "" || seen[s.Family] {
			continue
		}
		seen[s.Family] = true
		out = append(out, s.Family)
		if len(out) == maxAutoPackages {
			break
		}
	}
	return out
}

func searchViaDaemon(ctx context.Context, home string, req domain.SearchRequest) (*domain.SearchResponse, error) {
	c, err := daemon.NewClient(home)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	_, err = c.Status(pctx)
	cancel()
	if err != nil {
		return nil, err
	}
	resp, err := c.Search(ctx, req)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// renderSearchText prints the §11.5 layout:
//
//	MATCH: COMPATIBLE
//	CONFIDENCE: HIGH
//
//	Exact
//	- axios 1.12
//	...
func renderSearchText(w io.Writer, resp domain.SearchResponse) {
	if resp.Miss || len(resp.Results) == 0 {
		fmt.Fprintln(w, "MATCH: NO_SAFE_MATCH")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "No sample or evidence fits this environment safely.")
		fmt.Fprintln(w, "A wrong HIT is worse than a MISS (goal §3.8).")
		return
	}
	for i, r := range resp.Results {
		if i > 0 {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "----")
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "MATCH: %s\n", r.Grade)
		fmt.Fprintf(w, "CONFIDENCE: %s\n", r.Confidence)
		if r.Case != nil && r.Case.Goal != "" {
			fmt.Fprintf(w, "GOAL: %s\n", r.Case.Goal)
		}
		if r.SampleID != "" {
			fmt.Fprintf(w, "SAMPLE: %s (%s)\n", r.SampleID, r.SampleStatus)
		}
		printList(w, "Exact", r.Exact)
		printList(w, "Different", r.Different)
		printList(w, "Adaptation needed", r.Adaptation)

		fmt.Fprintln(w)
		fmt.Fprintln(w, "Evidence")
		fmt.Fprintf(w, "- Project compile observations: %d\n", r.Evidence.ProjectCompileObservations)
		fmt.Fprintf(w, "- Clean builds: %d\n", r.Evidence.CleanBuilds)
		fmt.Fprintf(w, "- Contract passes: %d\n", r.Evidence.ContractPasses)
		fmt.Fprintf(w, "- Independent cross peers: %d\n", r.Evidence.IndependentCrossPeers)
		if len(r.Evidence.ElevatedFailures) > 0 {
			fmt.Fprintf(w, "- Elevated failures: %s\n", strings.Join(r.Evidence.ElevatedFailures, ", "))
		}
		for _, kf := range r.KnownFailures {
			label := kf.ErrorCode
			if label == "" {
				label = kf.Fingerprint
			}
			fmt.Fprintf(w, "- Known failure: %s (%d observations)\n", label, kf.Count)
		}
	}
}

func printList(w io.Writer, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, title)
	for _, s := range items {
		fmt.Fprintf(w, "- %s\n", s)
	}
}
