package mcp

import (
	"context"
	"strings"
	"testing"
)

// report_sample_adoption checked only that the id was non-empty, so an agent
// could report adoption of a sample nothing ever returned. The row is queued
// for anonymous upload and becomes evidence about a sample that may not
// exist — and adoption is the number the whole "reasoning avoided" figure on
// the public site is derived from.
func TestAdoptionRejectsAnIdThatIsNotAContentAddress(t *testing.T) {
	called := false
	deps := &Deps{
		ReportAdoption: func(_ context.Context, _ string, _ bool, _ *bool) error {
			called = true
			return nil
		},
	}
	c := startServer(t, deps)

	for _, bad := range []string{
		"sha256:abc",
		"not-an-id",
		"sha256:" + strings.Repeat("g", 64),
		"sha256:" + strings.Repeat("AB", 32),
		strings.Repeat("ab", 32),
	} {
		res := callTool(t, c, "report_sample_adoption", map[string]any{
			"sampleId": bad, "applied": true,
		})
		if !strings.Contains(toolText(t, res), "sha256:") {
			t.Errorf("%q was not rejected with an explanation: %s", bad, toolText(t, res))
		}
		if called {
			t.Fatalf("%q reached ReportAdoption", bad)
		}
	}
}
