package web

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
)

// frontierSnap is a snapshot carrying the three frontier products exactly as
// the builder serializes them, so the page decodes the producer's shape and
// not a hand-written imitation of it.
func frontierSnap(purl, symbol string, snap compatibility.Snapshot) string {
	snap.SchemaVersion, snap.PURL, snap.Symbol = 1, purl, symbol
	snap.GeneratedAt = "2026-09-17T00:00:00Z"
	if snap.Rows == nil {
		snap.Rows = []compatibility.SnapshotRow{}
	}
	if snap.Failures == nil {
		snap.Failures = []compatibility.FailureSummary{}
	}
	b, err := json.Marshal(snap)
	if err != nil {
		panic(err)
	}
	return string(b)
}

const frontierCase = "case:0123456789abcdef0123456789abcdef"

func frontierStore() *fakeStore {
	f := newCubeStore()
	f.versions["npm|axios"] = []string{"2.0.0", "1.12.0", "1.11.0"}
	f.symbols["npm|axios|1.11.0"] = []string{"axios.post"}
	f.symbols["npm|axios|1.12.0"] = []string{"axios.post"}
	f.symbols["npm|axios|2.0.0"] = []string{"axios.post"}
	// 1.11.0: a working release with a measured path up, plus an
	// observation-only candidate that must render apart from it.
	f.snapshots[snapKey("pkg:npm/axios@1.11.0", "axios.post")] = frontierSnap("pkg:npm/axios@1.11.0", "axios.post",
		compatibility.Snapshot{
			SafeUpgradeCandidates: []compatibility.SafeUpgradeCandidate{{
				Package: "pkg:npm/axios@1.11.0", TargetPackage: "pkg:npm/axios@1.12.0",
				CaseID: frontierCase, Symbol: "axios.post", CurrentResult: "PASS", Stage: "CONTRACT",
				VerifierAdapter: "node-typescript@1", SandboxCapability: "CONTAINER_RUN",
				ContextLabel: "node 22", EnvBucketHash: "sha256:env",
				MeasuredPath: []string{"1.12.0"}, BlockedByVersion: "2.0.0",
				CurrentObservations: 1, TargetObservations: 2,
			}},
			RegressionCandidates: []compatibility.RegressionCandidate{{
				Package: "pkg:npm/axios@1.11.0", PreviousPackage: "pkg:npm/axios@1.10.0",
				Stage: "PROJECT_COMPILE", ContextLabel: "node 22",
				FailRate: 0.4, PreviousPassRate: 0.95, Observations: 10, PreviousObservations: 20,
			}},
		})
	// 2.0.0: the failing side of a measured boundary.
	f.snapshots[snapKey("pkg:npm/axios@2.0.0", "axios.post")] = frontierSnap("pkg:npm/axios@2.0.0", "axios.post",
		compatibility.Snapshot{
			RegressionCandidates: []compatibility.RegressionCandidate{{
				Package: "pkg:npm/axios@2.0.0", PreviousPackage: "pkg:npm/axios@1.12.0",
				CaseID: frontierCase, Symbol: "axios.post", Stage: "CONTRACT",
				VerifierAdapter: "node-typescript@1", SandboxCapability: "CONTAINER_RUN",
				CompanionPackages: []string{"pkg:npm/follow-redirects@1.15.6"},
				ContextLabel:      "node 22", EnvBucketHash: "sha256:env",
				FailRate: 1, PreviousPassRate: 1, Observations: 1, PreviousObservations: 2,
			}},
		})
	// 1.12.0: passed twice, then failed — flagged for revalidation.
	f.snapshots[snapKey("pkg:npm/axios@1.12.0", "axios.post")] = frontierSnap("pkg:npm/axios@1.12.0", "axios.post",
		compatibility.Snapshot{
			RegressionWatchCandidates: []compatibility.RegressionWatchCandidate{{
				Package: "pkg:npm/axios@1.12.0", CaseID: frontierCase, Symbol: "axios.post",
				Stage: "CONTRACT", Kind: compatibility.WatchKnownGoodNowFailing,
				VerifierAdapter: "node-typescript@1", SandboxCapability: "CONTAINER_RUN",
				ContextLabel: "node 22", EnvBucketHash: "sha256:env",
				KnownResult: "PASS", KnownObservations: 2, KnownThrough: "2026-09-10T12:00:00Z",
				LatestResult: "FAIL", LatestObservations: 1, LatestSince: "2026-09-16T12:00:00Z",
				NearestLowerPassPackage: "pkg:npm/axios@1.11.0",
			}},
		})
	return f
}

// The three products render on the release page, each under its own chip,
// with the conditions that measured it and a link to the release it names.
// The observation candidate is there too, last and labelled as inferred.
func TestVersionPageRendersTheVerificationFrontier(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = frontierStore() })

	res := get(t, mux, "/npm/axios/1.11.0")
	if res.Code != 200 {
		t.Fatalf("status = %d", res.Code)
	}
	body := res.Body.String()
	// Chips are matched with their markup: the section note names every
	// kind of claim in prose, and prose is not a rendered row.
	for _, want := range []string{
		"Verification frontier",
		`mono">safe upgrade</span>`, "measured passes reach", `href="/npm/axios/1.12.0"`,
		"measured path: 1.12.0", "stops there: 2.0.0 failed",
		`mono">inferred candidate</span>`, "observed, not measured: 40% failure rate here against 95% pass rate on",
		`href="/npm/axios/1.10.0"`, "PROJECT_COMPILE",
		"node-typescript@1", "CONTAINER_RUN", "1 vs 2 runs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("release page lacks %q", want)
		}
	}
	if strings.Contains(body, `mono">measured boundary</span>`) {
		t.Error("an observation candidate was dressed as a measured boundary")
	}
	if strings.Index(body, `mono">safe upgrade</span>`) > strings.Index(body, `mono">inferred candidate</span>`) {
		t.Error("the inferred candidate rendered ahead of the measured claim")
	}

	res = get(t, mux, "/npm/axios/2.0.0")
	body = res.Body.String()
	for _, want := range []string{
		`mono">measured boundary</span>`, "failed on this release; the last measured pass is",
		`href="/npm/axios/1.12.0"`, "follow-redirects@1.15.6", "2 vs 1 runs",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("boundary page lacks %q", want)
		}
	}

	res = get(t, mux, "/npm/axios/1.12.0")
	body = res.Body.String()
	for _, want := range []string{
		`mono">regression watch</span>`,
		"passed ×2 through 2026-09-10, then failed ×1 since 2026-09-16 — revalidate",
		"last measured pass below: 1.11.0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("watch page lacks %q", want)
		}
	}
}

// The symbol page reads its own snapshot and shows the same frontier.
func TestSymbolPageRendersTheVerificationFrontier(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = frontierStore() })
	res := get(t, mux, "/npm/axios/1.11.0/axios.post")
	if res.Code != 200 {
		t.Fatalf("status = %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{"Verification frontier", `mono">safe upgrade</span>`, "measured path: 1.12.0"} {
		if !strings.Contains(body, want) {
			t.Errorf("symbol page lacks %q", want)
		}
	}
}

// A release the receipts say nothing about renders no frontier at all: an
// empty section would read as "nothing to move to", which is a claim.
func TestReleaseWithoutFrontierEvidenceShowsNoFrontier(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	res := get(t, mux, "/npm/reactish/19.1.0")
	if res.Code != 200 {
		t.Fatalf("status = %d", res.Code)
	}
	if strings.Contains(res.Body.String(), "Verification frontier") {
		t.Fatal("frontier heading rendered with nothing under it")
	}
}

// Every locale renders the frontier in its own words: no key leaks and no
// English fallback where a translation exists.
func TestFrontierRendersInEveryLocale(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = frontierStore() })
	for _, lang := range []string{"ko", "ja", "zh-CN", "es", "fr", "de", "pt-BR", "ru"} {
		res := get(t, mux, "/npm/axios/1.11.0?lang="+lang)
		if res.Code != 200 {
			t.Fatalf("%s: status = %d", lang, res.Code)
		}
		body := res.Body.String()
		if strings.Contains(body, "frontier.") {
			t.Errorf("%s: a translation key leaked onto the page", lang)
		}
		if strings.Contains(body, "Verification frontier") {
			t.Errorf("%s: heading fell back to English", lang)
		}
	}
}
