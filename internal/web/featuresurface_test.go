package web

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/mcp"
)

// The features page says it documents the complete public MCP surface. It
// listed eight tools; the server registers ten.
//
// report_anomaly and report_csx_issue were added to the server and never to
// the page, and nothing could notice: the page's list is hand-written, and the
// test that guarded it was a hand-written list too, so the two agreed with
// each other while both disagreed with the server. This asks the server.
func TestTheFeaturesPageDocumentsEveryToolTheServerRegisters(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/features").Body.String()

	names := mcp.ToolNames()
	if len(names) == 0 {
		t.Fatal("the server registers no tools; this test would pass vacuously")
	}
	for _, name := range names {
		if !strings.Contains(body, `id="tool-`+name+`"`) {
			t.Errorf("the server registers %s and the page that claims to be complete does not document it", name)
		}
	}
}
