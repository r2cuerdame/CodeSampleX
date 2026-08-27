package web

import (
	"strings"
	"testing"
)

// The point of the derived group is that the page grows without anyone
// editing findings.go, so the test that matters is: a published sample that
// states a belief appears on /findings, with its sample link.
func TestFindingsIncludesDerivedSamples(t *testing.T) {
	mux, f := newTestMux(t, nil)
	f.derived = []DerivedFinding{{
		Ecosystem: "pypi",
		Subject:   "httpx@0.28.1",
		Believed:  "a timeout of 5 covers the whole request",
		Measured:  "connect, read, write and pool each get their own 5 seconds",
		SampleID:  "sha256:aaaa000000000000000000000000000000000000000000000000000000000001",
	}}
	body := getEventually(t, mux, "/findings", "httpx@0.28.1").Body.String()
	mustContain(t, body, "Stated by the sample, measured by its contract")
	mustContain(t, body, "httpx@0.28.1")
	mustContain(t, body, "a timeout of 5 covers the whole request")
	mustContain(t, body,
		"/samples/sha256:aaaa000000000000000000000000000000000000000000000000000000000001")
}

// With nothing derived the heading must not appear at all. An empty
// section under a promise that the network is finding things is worse than
// no section, and this is the state the page ships in.
func TestFindingsHidesEmptyDerivedGroup(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/findings").Body.String()
	if strings.Contains(body, "Stated by the sample, measured by its contract") {
		t.Error("the derived heading must be absent when no sample declares a belief")
	}
	// The hand-written groups still render — they are the page's substance
	// and must not depend on the store answering at all.
	mustContain(t, body, "Contradicts an official source")
}

// The count in the page header has to include the derived rows, or the
// page says "29 findings" above thirty of them.
func TestFindingsCountIncludesDerived(t *testing.T) {
	mux, f := newTestMux(t, nil)
	base := countFromBody(t, get(t, mux, "/findings").Body.String())
	f.derived = []DerivedFinding{{
		Ecosystem: "npm",
		Subject:   "ws@8.19.0",
		Believed:  "close() waits for the peer",
		Measured:  "close() returns before the close frame is acknowledged",
		SampleID:  "sha256:bbbb000000000000000000000000000000000000000000000000000000000002",
	}}
	// The handler caches for derivedTTL, so a second site is what a fresh
	// process would see.
	mux2, f2 := newTestMux(t, nil)
	f2.derived = f.derived
	if got := countFromBody(t, getEventually(t, mux2, "/findings", "ws@8.19.0").Body.String()); got != base+1 {
		t.Errorf("count = %d, want %d: the derived rows are findings too", got, base+1)
	}
}

// countFromBody reads the leading number out of "N findings across M
// ecosystems."
func countFromBody(t *testing.T, body string) int {
	t.Helper()
	i := strings.Index(body, " findings across ")
	if i < 0 {
		t.Fatal("the page no longer states how many findings it has")
	}
	start := i
	for start > 0 && body[start-1] >= '0' && body[start-1] <= '9' {
		start--
	}
	n := 0
	for _, c := range body[start:i] {
		n = n*10 + int(c-'0')
	}
	return n
}
