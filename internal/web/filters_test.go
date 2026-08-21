package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestBrowserExecutionContextTakesPriorityOverToolRuntime(t *testing.T) {
	env := domain.EnvironmentFingerprint{
		Runtime: "node", ExecutionContext: "browser", BrowserFamily: "chrome",
	}
	if got := RecordEnvironmentRuntime(env); got != "browser" {
		t.Fatalf("runtime bucket = %q, want browser", got)
	}
}

func TestHexRuntimeFilterUsesRecordedElixir(t *testing.T) {
	env := domain.EnvironmentFingerprint{Runtime: "elixir", RuntimeVersion: "1.19"}
	if got := RecordEnvironmentRuntime(env); got != "elixir" {
		t.Fatalf("runtime bucket = %q, want elixir", got)
	}
	mux, store := newTestMux(t, nil)
	store.packages = append(store.packages, PackageHit{
		Ecosystem: "hex", Name: "req", LatestVersion: "0.5.15",
		OperatingSystems: []string{"linux"}, Runtimes: []string{"elixir"},
		EvidenceBases: []string{"verified"},
	})
	// The dropdown is gone -- runtime is very nearly a restatement of the
	// ecosystem (npm is node, pypi is python, golang is go), so it offered a
	// second control for a choice already made. The PARAMETER still filters,
	// because links carrying it were published and must keep working.
	body := get(t, mux, "/records?eco=hex&runtime=elixir").Body.String()
	mustContain(t, body, `href="/hex/req"`)
	mustNotContain(t, body, `<select name="runtime">`)
	mustNotContain(t, body, `<select name="basis">`)
}

func TestMavenJavaFiltersAreAccepted(t *testing.T) {
	f := cleanRecordFilter(RecordFilter{Ecosystem: "maven", Runtime: "java"})
	if f.Ecosystem != "maven" || f.Runtime != "java" {
		t.Fatalf("Maven/Java filters were discarded: %+v", f)
	}
}

func TestRecordQueryMatchesSeveralPackageNamesAtOnce(t *testing.T) {
	query := ParseRecordQuery(" React, axios  lodash | react ")
	for _, name := range []string{"react", "react-dom", "axios", "lodash-es"} {
		matched, _, _ := query.MatchPackage(name)
		if !matched {
			t.Errorf("batch query did not match %q", name)
		}
	}
	if matched, _, _ := query.MatchPackage("vue"); matched {
		t.Error("batch query matched an unrelated package")
	}
	if _, exact, _ := query.MatchPackage("react"); !exact {
		t.Error("exact package name was not identified for ranking")
	}
}

func TestRecordsFiltersUseRecordedDimensions(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/records?eco=npm&os=windows&runtime=node&basis=verified").Body.String()
	mustContain(t, body, `href="/npm/axios"`)
	if strings.Contains(body, "github.com/a/b") {
		t.Error("record without the selected runtime/basis was shown")
	}
	mustContain(t, body, `<option value="windows" selected>`)
}

func TestFindingsEnvironmentAndBasisFilters(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.derived = []DerivedFinding{{
		Ecosystem: "pypi", Subject: "httpx@0.28.1",
		Believed: "one timeout covers the whole request",
		Measured: "each phase gets its own timeout instead",
		SampleID: "sha256:" + strings.Repeat("a", 64),
		OS:       "linux", Runtime: "python", Environment: "python 3.13 · linux/amd64",
	}}
	body := get(t, mux, "/findings?eco=pypi&os=linux&runtime=python&basis=sample").Body.String()
	mustContain(t, body, "httpx@0.28.1")
	mustContain(t, body, "python 3.13 · linux/amd64")
	if strings.Contains(body, "Contradicts an official source</h2>") {
		t.Error("docs-basis findings were shown under the sample-basis filter")
	}
}

func TestSamplePageShowsEvidenceEnvironmentPanel(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()
	mustContain(t, body, "Execution evidence")
	// The basis says what the evidence IS, not what rung it earns. It used
	// to switch on the level ladder, so "independent cross-verification"
	// printed beside a single receipt.
	mustContain(t, body, "Signed contract pass")
	mustContain(t, body, "linux")
	mustContain(t, body, "amd64")
	mustContain(t, body, "node 22.18")
}

func TestWantedPackageHasHonestStubPage(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{{Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Scene", Asks: 3}}
	rec := get(t, mux, "/npm/three?lang=ko")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "아래 버전·API 좌표는 아직 답을 얻지 못했습니다")
	mustContain(t, body, "@0.180.0")
	mustContain(t, body, "Scene")
}
