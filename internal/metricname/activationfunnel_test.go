package metricname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The activation document is an implementation contract. These boundaries
// were previously phrased loosely enough to count a new search as a transport
// retry, process startup as MCP readiness, and a later home-using command as
// the first binary execution.
func TestActivationFunnelKeepsEventAndLifecycleBoundaries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "activation-funnel.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, want := range []string{
		"same queued hit payload carries the same\noffer ID and collapses as a transport retry",
		"new random offer ID, and is a distinct hit",
		"same stdio\nsession has successfully answered a valid `initialize` request and then\nreceived `notifications/initialized`",
		"at the top of `cli.Main`, before it\ninspects or dispatches `argv`",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("activation contract lost boundary %q", want)
		}
	}
	for _, stale := range []string{
		"`csx mcp` at\n`newDeps`",
		"`config.EnsureHome` first creates the directory tree",
		"search retried all afternoon is one hit",
	} {
		if strings.Contains(doc, stale) {
			t.Errorf("activation contract restored stale claim %q", stale)
		}
	}
}
