package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// A transient final response leaves one classified line in the server log
// (#445), so production can be read for "timeout -> 404 = 0" from `docker
// logs` alone: the line carries the exact totals even when the per-second
// throttle suppresses its neighbours, and a proven 404 never writes one.
func TestTransientFinalLogsClassifiedTotals(t *testing.T) {
	ResetRouteMetrics()
	var lines []string
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	prev := transientLog
	transientLog = &routeOutcomeLog{
		now: func() time.Time { return now },
		out: func(format string, v ...any) { lines = append(lines, fmt.Sprintf(format, v...)) },
	}
	defer func() { transientLog = prev }()

	mux, f := newTestMux(t, nil)

	// A proven 404 is not a transient outcome: no line.
	if got := get(t, mux, "/samples/sha256:0000000000000000000000000000000000000000000000000000000000000000").Code; got != http.StatusNotFound {
		t.Fatalf("absent sample status = %d, want 404", got)
	}
	if len(lines) != 0 {
		t.Fatalf("proven 404 wrote a transient line: %q", lines)
	}

	// Two refused reads inside one second: one line, exact totals.
	f.sampleMetaErr = serverstore.ErrPoolBusy
	get(t, mux, "/samples/sha256:d1e2f3")
	get(t, mux, "/samples/sha256:d1e2f3")
	if len(lines) != 1 {
		t.Fatalf("got %d lines inside the throttle window, want 1: %q", len(lines), lines)
	}
	for _, token := range []string{
		"web: transient final", "path=/samples/sha256:d1e2f3", "status=503",
		"proven_not_found_total=1", "pool_busy_total=1", "final_503_total=1", "final_504_total=0",
	} {
		if !strings.Contains(lines[0], token) {
			t.Errorf("line missing %q: %s", token, lines[0])
		}
	}

	// Past the window the next final writes again, and its totals include
	// the suppressed one.
	now = now.Add(2 * time.Second)
	get(t, mux, "/samples/sha256:d1e2f3")
	if len(lines) != 2 {
		t.Fatalf("got %d lines after the window, want 2: %q", len(lines), lines)
	}
	for _, token := range []string{"pool_busy_total=3", "final_503_total=3", "proven_not_found_total=1"} {
		if !strings.Contains(lines[1], token) {
			t.Errorf("second line missing %q: %s", token, lines[1])
		}
	}
}
