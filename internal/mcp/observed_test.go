package mcp

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// USED records that a package was installed; nothing was run. This release
// scrubbed that inflation from every rate — the snapshot's confidence, the
// web's pass rates — and the relay then printed it back to agents as "8697 of
// 8697 passed", on the surface where a green number does the most damage.
// USED cells carry the highest reporter counts and zero Fail, so they sort
// first among the relayed cells: the relay's lead line was the dishonest one.
func TestRelayedPresenceIsNotRenderedAsPassingRuns(t *testing.T) {
	o := &domain.ObservedReports{
		PURL: "pkg:npm/left-pad@1.3.0",
		Cells: []domain.ObservedCell{
			{
				Environment: domain.ObservedEnvironment{OS: "linux", Runtime: "node", RuntimeVersion: "22"},
				Stage:       string(domain.StageUsed),
				Pass:        8697, Fail: 0, Reporters: 40,
			},
			{
				Environment: domain.ObservedEnvironment{OS: "linux", Runtime: "node", RuntimeVersion: "22"},
				Stage:       string(domain.StageProjectCompile),
				Pass:        297, Fail: 15, Reporters: 12,
			},
		},
	}
	out := renderObserved(o)
	if strings.Contains(out, "8697 of 8697 passed") {
		t.Errorf("presence rendered as passing runs:\n%s", out)
	}
	// The count is still worth relaying — as what it is.
	if !strings.Contains(out, "8697") {
		t.Errorf("the presence count was dropped entirely:\n%s", out)
	}
	if !strings.Contains(out, "nothing was run") {
		t.Errorf("the presence line does not say nothing was run:\n%s", out)
	}
	// A real command stage keeps its honest rate.
	if !strings.Contains(out, "297 of 312 passed") {
		t.Errorf("a real run stopped being a rate:\n%s", out)
	}
}
