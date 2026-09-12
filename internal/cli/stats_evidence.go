package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// evidenceStatsArgs is intentionally exact: only these valid, explicitly
// read-only invocations are exempt from the usual first-run activation write.
func evidenceStatsArgs(args []string) (jsonOut, ok bool) {
	if len(args) == 1 && args[0] == "--evidence-only" {
		return false, true
	}
	if len(args) == 2 && ((args[0] == "--evidence-only" && args[1] == "--json") ||
		(args[0] == "--json" && args[1] == "--evidence-only")) {
		return true, true
	}
	return false, false
}

func evidenceStatsMain(ctx context.Context, jsonOut bool, stdout, stderr io.Writer) int {
	home, err := config.Home()
	if err != nil {
		fmt.Fprintln(stderr, "csx: evidence stats unavailable")
		return 1
	}
	st, err := localdb.ReadEvidenceStats(ctx, filepath.Join(home, "csx.db"))
	if err != nil {
		// Raw SQLite errors may contain a private path or corrupt stored data.
		// No successful-looking counters precede this failure.
		fmt.Fprintln(stderr, "csx: evidence stats unavailable")
		return 1
	}
	if jsonOut {
		if err := json.NewEncoder(stdout).Encode(st); err != nil {
			return 1
		}
		return 0
	}
	var text bytes.Buffer
	fmt.Fprintf(&text, "  Pending evidence batches:    %d\n", st.Queue.EvidenceBatches)
	fmt.Fprintf(&text, "  Pending upload reports:      %d\n", st.Queue.Uploads)
	fmt.Fprintf(&text, "  Pending queue depth:         %d\n", st.QueueDepth)
	fmt.Fprintf(&text, "Evidence refused for good:     %d (the server will not accept these)\n", st.EvidenceRefusedTerminal)
	if st.LastUpload != "" {
		fmt.Fprintf(&text, "Last upload:                   %s\n", st.LastUpload)
	}
	if st.LastUploadAttempt != "" {
		fmt.Fprintf(&text, "Last upload attempt:           %s\n", st.LastUploadAttempt)
	}
	if st.LastUploadError != "" {
		fmt.Fprintf(&text, "Last upload error:             %s\n", st.LastUploadError)
	}
	if _, err := io.Copy(stdout, &text); err != nil {
		return 1
	}
	return 0
}

func runEvidenceStats(ctx context.Context, args []string) (int, bool) {
	jsonOut, ok := evidenceStatsArgs(args)
	if !ok {
		return 0, false
	}
	return evidenceStatsMain(ctx, jsonOut, os.Stdout, os.Stderr), true
}
