package web

import (
	"net/http/httptest"
	"testing"
)

// The Failure Issue puts three things on a phone that nothing else on the
// site puts there together: a release-verdict list, a boundary panel whose
// heading is two version strings and an arrow, and the dependency matrix.
// Every one of them is unbreakable text in a monospace face, which is the
// shape that overflowed the sample page in R2C-196 — and a stylesheet read as
// text cannot tell whether it fits. This measures the geometry.
//
// It does not reuse measurePage: that helper also requires a tooltip to be
// open and measurable, and this page carries none.
func TestFailureIssueFitsNarrowViewports(t *testing.T) {
	chrome := findChrome(t)

	mux, f := newTestMux(t, func(d *Deps) {
		// The deployed footer prints the release commit — 40 unbroken hex
		// characters, which the short fixture version would have hidden.
		d.Build.Revision = "3ca13b91c900cb721572f35bdae81dcc3c61e433"
	})
	clusters := seedFailureIssueFixture(t, f)
	id := issueIDFor(t, clusters, "sha256:aaa11122233344455566677788899900")

	srv := httptest.NewServer(measureHarness(mux, "/npm/libx?issue="+id))
	defer srv.Close()

	for _, r := range measureViewports(t, chrome, srv.URL+measurePath) {
		if got := r.Closed.overflow(); got > 0 {
			t.Errorf("viewport %dpx: documentElement.scrollWidth %.0f > clientWidth %.0f (%.0fpx of horizontal overflow)",
				r.Width, r.Closed.ScrollWidth, r.Closed.ClientWidth, got)
		}
		for _, o := range r.Closed.Offenders {
			t.Errorf("viewport %dpx: %s reaches x=%.0f past clientWidth %.0f (left=%.0f width=%.0f) %q",
				r.Width, o.Sel, o.Right, r.Closed.ClientWidth, o.Left, o.Width, o.Text)
		}
	}
}
