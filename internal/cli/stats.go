package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/daemon"
)

func init() {
	Register(Command{
		Name:    "stats",
		Summary: "show the local dashboard numbers: csx stats [--json]",
		Run:     statsMain,
	})
}

// statsMain implements `csx stats [--json]`. All numbers are computed
// locally (via the daemon when running, else directly); the
// reasoning-avoided figure is always labeled as an estimate (§12.5).
func statsMain(ctx context.Context, args []string) int {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		default:
			fmt.Fprintln(os.Stderr, "usage: csx stats [--json]")
			return 2
		}
	}

	home, err := config.Home()
	if err != nil {
		fmt.Fprintf(os.Stderr, "csx: %v\n", err)
		return 1
	}
	st, err := statsViaDaemon(ctx, home)
	if err != nil {
		d, derr := daemon.New(home)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "csx: stats: %v\n", derr)
			return 1
		}
		defer d.Close()
		s, serr := d.StatsNow(ctx)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "csx: stats: %v\n", serr)
			return 1
		}
		st = &s
	}

	if jsonOut {
		out, err := json.MarshalIndent(st, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "csx: %v\n", err)
			return 1
		}
		fmt.Println(string(out))
		return 0
	}

	passHuman := "-"
	if st.PostHitBuildReports > 0 {
		passHuman = fmt.Sprintf("%.0f%% (%d reports)", st.PostHitBuildPassRate*100, st.PostHitBuildReports)
	}
	fmt.Printf("Mode:                          %s\n", modeOrUninitialized(st.Mode))
	fmt.Printf("Hits / Misses:                 %d / %d\n", st.Hits, st.Misses)
	fmt.Printf("Adoptions:                     %d\n", st.Adoptions)
	fmt.Printf("Post-hit build pass:           %s\n", passHuman)
	fmt.Printf("Estimated reasoning avoided:   %d  (Estimated — never measured)\n", st.EstimatedReasoningAvoided)
	fmt.Printf("Automatic evidence sent:       %d batches\n", st.EvidenceBatchesSent)
	fmt.Printf("Origin seeds:                  %d\n", st.OriginSeeds)
	fmt.Printf("Cross verifications:           %d\n", st.CrossVerifications)
	fmt.Printf("Local cache:                   %.1f MB (budget %d MB)\n", float64(st.CacheBytes)/(1<<20), st.CacheBudgetMB)
	fmt.Printf("Known packages:                %d\n", st.Packages)
	fmt.Printf("Pending queue depth:           %d\n", st.QueueDepth)
	if st.LastUpload != "" {
		fmt.Printf("Last upload:                   %s\n", st.LastUpload)
	}
	if st.LastUploadAttempt != "" {
		fmt.Printf("Last upload attempt:           %s\n", st.LastUploadAttempt)
	}
	if st.LastUploadError != "" {
		fmt.Printf("Last upload error:             %s\n", st.LastUploadError)
	}
	printReadiness(os.Stdout, st.Readiness)
	return 0
}

// printReadiness renders the local activation ledger
// (docs/activation-funnel.md §7). The rules are inherited from
// `csx hook status`, because they were already right there:
//
//   - it may only say what it can show — every row names where it was read
//     from, and none of them claims anything about anyone else's install;
//   - an unreached stage is a gap, never a zero: "never" with the command
//     that fixes it, not a formatted 1970;
//   - "never seen" is distinct from "not working". An MCP row with no
//     completed handshake means no client has finished the protocol
//     lifecycle here. It is not a claim that the path is broken.
//
// Nothing on this panel is uploaded in any mode, and the header says so
// where a reader will see it — the panel is about their machine, and the
// first question a privacy-minded reader has about a new panel is whether it
// is also about someone else's.
func printReadiness(w io.Writer, r daemon.Readiness) {
	fmt.Fprintf(w, "\nReadiness                      (local only — nothing here is uploaded)\n")
	rows := []struct {
		label, value, source, next string
	}{
		{"First run", r.FirstRunAt, "csx.db", ""},
		{"Initialized", r.InitAt, "csx.db", "run csx init"},
		{"Shard cache warmed", r.FirstSyncAt, "csx.db", "run csx sync"},
		{"MCP handshake", r.MCPFirstReadyAt, "csx.db", "restart your coding agent, then use a csx tool"},
		{"First answer", r.FirstHitAt, "csx.db", "ask an agent to search before writing library code"},
		{"First adoption", r.FirstAdoptionAt, "csx.db", "report_sample_adoption after using a sample"},
	}
	for _, row := range rows {
		if row.value == "" {
			line := fmt.Sprintf("  %-28s —  never", row.label)
			if row.next != "" {
				line += "  → " + row.next
			}
			fmt.Fprintln(w, line)
			continue
		}
		fmt.Fprintf(w, "  %-28s %s  (%s)\n", row.label, row.value, row.source)
	}
	if r.MCPLastReadyAt != "" && r.MCPLastReadyAt != r.MCPFirstReadyAt {
		fmt.Fprintf(w, "  %-28s %s  (csx.db)\n", "MCP last handshake", r.MCPLastReadyAt)
	}
	// The one duration this product can honestly measure (§5): both endpoints
	// are on this machine, and the server holds neither in a form that
	// survives a UTC day boundary. It is rendered here and never uploaded.
	if r.SecondsToFirstAnswer != nil {
		fmt.Fprintf(w, "  %-28s %s after csx init\n", "Time to first answer",
			(time.Duration(*r.SecondsToFirstAnswer) * time.Second).String())
	}
}

func statsViaDaemon(ctx context.Context, home string) (*daemon.Stats, error) {
	c, err := daemon.NewClient(home)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// A daemon left over from another build answers with that build's fields,
	// and the activation funnel is exactly the kind of new field an older one
	// returns empty — the panel then reports an install as having reached no
	// stage at all. So the version is checked before the answer is used.
	//
	// Checked, not corrected. csx ui may stop and replace a mismatched daemon
	// because starting one is what that command is for; `csx stats` is a read
	// and must not start a background service as a side effect. It reports the
	// mismatch as an error and the caller falls back to reading the local
	// store, which is where these numbers live anyway.
	//
	// The first attempt at this used EnsureRunning here, and that spawns
	// os.Executable() as `daemon run`. Inside a test binary os.Executable() IS
	// the test binary, so each spawn re-ran the suite and spawned again: 348
	// processes off one `go test ./internal/cli/` before it was killed.
	info, err := c.Status(pctx)
	if err != nil {
		return nil, err
	}
	if info.Version != "" && Version != "" && info.Version != Version {
		return nil, fmt.Errorf("daemon is build %s, this is %s", info.Version, Version)
	}
	st, err := c.Stats(pctx)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
