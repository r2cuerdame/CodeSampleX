package web

import (
	"strings"
	"testing"
)

// A failure cluster is a fact about one release in one environment, and the
// row named the environment but not the release. The version showed only when
// the cluster was flagged as a regression candidate, so an ordinary cluster —
// which is most of them — never said what it happened to.
//
// The reader has usually drilled to a version to get here, but a cluster's own
// versions are not always the one pinned: a cluster spanning two releases is
// what a regression looks like before anyone calls it one.
func TestAClusterSaysWhichVersionItHappenedTo(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?f_version=1.12.0").Body.String()

	// The ordinary cluster, not the regression candidate beside it.
	at := strings.Index(body, "sha256:ddeeff")
	if at < 0 {
		t.Fatal("the ordinary cluster is not on the page at all")
	}
	start := strings.LastIndex(body[:at], `<li class="cluster">`)
	if start < 0 {
		t.Fatal("could not find the cluster row")
	}
	end := strings.Index(body[start:], "</li>")
	block := body[start : start+end]
	if !strings.Contains(block, "1.12.0") {
		t.Errorf("the cluster row does not name its version:\n%s", block)
	}
}
