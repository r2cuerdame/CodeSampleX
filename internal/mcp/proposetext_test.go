package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// renderPropose drives the real handler and returns its reply text.
func renderPropose(t *testing.T) string {
	t.Helper()
	deps := emptyDeps()
	deps.Propose = func(_ context.Context, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
		spec := samples.BuildSpec(samples.ScanInputs{
			Goal: goal, Kind: "HOW", Packages: pkgs, Symbols: symbols})
		return spec, spec.PromptText(), `C:\fake\work\sample-1`, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "axios upload with progress",
		"packages": []string{"pkg:npm/axios@1.12.0"},
	})
	return toolText(t, res)
}

// An agent that cannot write to the workspace must stop, not improvise.
//
// Observed in the field: the workspace was a Windows path under the
// daemon's home, the calling agent was in a Linux container, and it wrote a
// complete sample to /root/csx_layout_sample instead. Nothing errored. The
// sample was simply somewhere `csx sample create` would never be pointed
// at, and nobody found out.
func TestProposeTellsTheAgentToStopRatherThanPickItsOwnPath(t *testing.T) {
	out := renderPropose(t)
	for _, want := range []string{
		"IF YOU CANNOT WRITE TO THAT EXACT PATH",
		"STOP and say so",
		"Do not choose a different directory silently",
		"say exactly where",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the reply no longer says %q:\n%s", want, out)
		}
	}
}

// Upload is seeded-only. Handing every caller a `publish` command sends
// most of them into a 403, and says nothing about what the sample is
// actually worth to them.
func TestProposeDoesNotPromiseAPublishItCannotDeliver(t *testing.T) {
	out := renderPropose(t)
	if strings.Contains(out, "csx sample publish <sampleId>") {
		t.Error("the reply still instructs an unseeded caller to publish")
	}
	for _, want := range []string{
		"stays on this machine",
		"seeded-only",
		"contribute the IDEA",
		"https://codesamplex.dev/wanted",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the reply no longer says %q:\n%s", want, out)
		}
	}
	// /contribute is retired and 404s; the reply must not send anyone there.
	if strings.Contains(out, "codesamplex.dev/contribute") {
		t.Error("the reply still points at the retired /contribute page")
	}
	// Reviewing every file before anything leaves is the step the whole
	// design rests on, and it must survive this change.
	if !strings.Contains(out, "csx sample preview") {
		t.Error("the review step is gone")
	}
}
