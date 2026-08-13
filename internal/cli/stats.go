package cli

import (
	"context"
	"encoding/json"
	"fmt"
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
	return 0
}

func statsViaDaemon(ctx context.Context, home string) (*daemon.Stats, error) {
	c, err := daemon.NewClient(home)
	if err != nil {
		return nil, err
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := c.Stats(pctx)
	if err != nil {
		return nil, err
	}
	return &st, nil
}
