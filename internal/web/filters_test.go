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
	body := get(t, mux, "/records?eco=hex&runtime=elixir").Body.String()
	mustContain(t, body, `href="/hex/req"`)
	mustContain(t, body, `<option value="elixir" selected>`)
	if strings.Contains(body, `<option value="beam"`) {
		t.Error("runtime filter still exposes an unrecorded beam bucket")
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
	mustContain(t, body, "Independent cross-verification")
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
