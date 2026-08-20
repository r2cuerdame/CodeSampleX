package web

import (
	"strings"
	"testing"
)

// The pair is reported by the machine that held the lockfile. The server
// receives one package per record, so a resolution arrives already shredded
// and only the scanner ever sees both versions together — which is why the
// earlier server-side version of this had to be reverted.
func TestPackagePageShowsVersionsFoundInOneResolution(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.coresidence = []VersionCoresidence{
		{Lower: "7.5.0", Higher: "8.19.0", Projects: 4, Failing: 3},
	}
	body := mustGet(t, mux, "/npm/axios")
	mustContain(t, body, `class="coresident"`)
	mustContain(t, body, "7.5.0")
	mustContain(t, body, "8.19.0")
}

// A pair nobody saw break is still worth knowing — two versions of one
// library in one install is the thing to look at when something breaks later
// — so it is listed, without a failure count it does not have.
func TestPairsThatNeverFailedAreListedWithoutAFailureCount(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.coresidence = []VersionCoresidence{
		{Lower: "1.0.0", Higher: "1.1.0", Projects: 9, Failing: 0},
	}
	body := mustGet(t, mux, "/npm/axios")
	i := strings.Index(body, `class="coresident"`)
	if i < 0 {
		t.Fatal("the block did not render")
	}
	block := body[i:]
	if j := strings.Index(block, "</ul>"); j >= 0 {
		block = block[:j]
	}
	if strings.Contains(block, "✕") {
		t.Error("a failure mark appeared for a pair that never failed")
	}
}

// Nothing to report is silence, not an empty heading.
func TestPackagePageStaysQuietWithNoCoresidence(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.coresidence = nil
	if strings.Contains(mustGet(t, mux, "/npm/axios"), `class="coresident"`) {
		t.Error("the block rendered with nothing in it")
	}
}
