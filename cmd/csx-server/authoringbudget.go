package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// runAuthoringBudgetReport measures what one coordinate costs the farm
// (#149). It reads read-only dumps of authoring_attempts and
// authoring_sessions rather than the database, so it can never write a
// ledger, never runs migrations, and replays exactly the rows an operator
// dumped — the SQL is in docs/authoring-quarantine.md.
func runAuthoringBudgetReport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("authoring-budget-report", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ledgerPath := fs.String("ledger", "", "JSON-lines dump of authoring_attempts (plain or base64 per line)")
	sessionsPath := fs.String("sessions", "", "JSON-lines dump of authoring_sessions (plain or base64 per line)")
	opts := serverstore.DefaultAuthoringBudgetOptions()
	fs.DurationVar(&opts.PrintTimeout, "print-timeout", opts.PrintTimeout, "the writer's per-iteration timeout (agy --print-timeout)")
	fs.DurationVar(&opts.TimeoutLike, "timeout-like", opts.TimeoutLike, "in-slot time from which an attempt without a sample counts as a timeout")
	fs.IntVar(&opts.Top, "top", opts.Top, "how many of the most expensive episodes to list")
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return 2
	}
	if *ledgerPath == "" || *sessionsPath == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: csx-server authoring-budget-report --ledger FILE --sessions FILE [--print-timeout 50m] [--timeout-like 45m] [--top N]")
		return 2
	}
	var rows []serverstore.AuthoringBudgetRow
	if err := readDumpLines(*ledgerPath, &rows); err != nil {
		fmt.Fprintf(stderr, "csx-server authoring-budget-report: %v\n", err)
		return 1
	}
	var sessions []serverstore.AuthoringBudgetSession
	if err := readDumpLines(*sessionsPath, &sessions); err != nil {
		fmt.Fprintf(stderr, "csx-server authoring-budget-report: %v\n", err)
		return 1
	}
	report, err := serverstore.MeasureAuthoringBudget(rows, sessions, opts)
	if err != nil {
		fmt.Fprintf(stderr, "csx-server authoring-budget-report: %v\n", err)
		return 1
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		fmt.Fprintf(stderr, "csx-server authoring-budget-report: %v\n", err)
		return 1
	}
	return 0
}

// readDumpLines decodes one JSON object per line. A line may be the object
// itself or its base64 encoding: psql COPY escapes backslashes in text
// format, so the documented dump base64-encodes each row.
func readDumpLines[T any](path string, out *[]T) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		if raw[0] != '{' {
			decoded, err := base64.StdEncoding.DecodeString(string(raw))
			if err != nil {
				return fmt.Errorf("%s:%d: neither JSON nor base64: %w", path, line, err)
			}
			raw = decoded
		}
		var v T
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
		*out = append(*out, v)
	}
	return scanner.Err()
}
