// Command csx-seo-report turns a Google Search Console export into the
// sample/package cohort comparison R2C-205 measures, and stores or reads the
// baseline it is compared against.
//
// It is an operator tool, run with `go run`, and it ships in no release:
// nothing about it belongs on a user's machine. Everything it reads is a
// file the operator downloaded; it talks to no network and to no database.
//
//	# store the pre-deploy baseline from a Search Console export
//	go run ./cmd/csx-seo-report -pages Pages.csv -queries Queries.csv \
//	    -label "2026-08-27 export" -out docs/seo/serp-baseline-2026-08-27.json
//
//	# after the deploy, measure the same cohorts and compare
//	go run ./cmd/csx-seo-report -pages Pages.csv -queries Queries.csv \
//	    -label "2026-09-24 export" -baseline docs/seo/serp-baseline-2026-08-27.json
//
// Exit status is 0 whenever the report was produced. It is deliberately not
// a pass/fail gate: whether a CTR movement is worth acting on is a judgement
// about ranking, seasonality and sample size, and a tool that answered it
// with an exit code would be inventing certainty it does not have.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/seoreport"
)

func main() {
	pages := flag.String("pages", "", "Search Console Pages CSV export (required)")
	queries := flag.String("queries", "", "Search Console Queries CSV export (optional)")
	label := flag.String("label", "", "what this export is, in your words (required)")
	source := flag.String("source", "", "where the numbers came from; defaults to the file names")
	baseline := flag.String("baseline", "", "stored baseline JSON to compare against")
	out := flag.String("out", "", "write this measurement to a baseline JSON file")
	flag.Parse()

	if err := run(*pages, *queries, *label, *source, *baseline, *out); err != nil {
		fmt.Fprintln(os.Stderr, "csx-seo-report:", err)
		os.Exit(1)
	}
}

func run(pagesPath, queriesPath, label, source, baselinePath, outPath string) error {
	if pagesPath == "" {
		return fmt.Errorf("-pages is required")
	}
	if label == "" {
		return fmt.Errorf("-label is required: a measurement with no export behind it cannot be compared later")
	}
	pages, err := readCSV(pagesPath)
	if err != nil {
		return err
	}
	var queries []seoreport.Row
	if queriesPath != "" {
		if queries, err = readCSV(queriesPath); err != nil {
			return err
		}
	}
	if source == "" {
		source = "parsed from " + filepath.Base(pagesPath)
		if queriesPath != "" {
			source += " and " + filepath.Base(queriesPath)
		}
	}
	snap := seoreport.Build(label, source, pages, queries)
	seoreport.RenderSnapshot(os.Stdout, snap)

	if baselinePath != "" {
		f, err := os.Open(baselinePath)
		if err != nil {
			return fmt.Errorf("open baseline: %w", err)
		}
		defer f.Close()
		before, err := seoreport.ReadJSON(f)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout)
		seoreport.RenderComparison(os.Stdout, before, snap, seoreport.Compare(before, snap))
	}
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		defer f.Close()
		if err := seoreport.WriteJSON(f, snap); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "\nwrote %s\n", outPath)
	}
	return nil
}

func readCSV(path string) ([]seoreport.Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	rows, err := seoreport.ParseCSV(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rows, nil
}
