package web

import (
	"strings"
	"testing"
)

// The version page is where a search result lands and where the cube's exact
// records link, and it showed no failures at all — go-isatty v0.0.24 carries
// thirteen and the page named none of them. Everything else about that
// release was there: which symbol ran where, the environments, the samples.
//
// A cluster names its own versions, so the page can show exactly the ones
// recorded against this release and leave the rest to the package page.
func TestTheVersionPageShowsThatVersionsFailures(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios/1.12.0").Body.String()

	if !strings.Contains(body, `<li class="cluster">`) {
		t.Fatal("the version page names none of the release's failures")
	}
	if !strings.Contains(body, "sha256:ddeeff") {
		t.Error("the cluster recorded against 1.12.0 is missing")
	}
}

// A failure recorded only against another release is not this release's.
func TestTheVersionPageLeavesOtherReleasesFailuresAlone(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.clusters["npm|axios"] = []string{`{
	  "stage": "PROJECT_TEST", "fingerprint": "sha256:elsewhere",
	  "observationCount": 4, "envSummary": {"os": "linux"},
	  "versions": ["1.11.0"]
	}`}
	body := get(t, mux, "/npm/axios/1.12.0").Body.String()
	if strings.Contains(body, "sha256:elsewhere") {
		t.Error("a failure from 1.11.0 is shown on the page for 1.12.0")
	}
}
