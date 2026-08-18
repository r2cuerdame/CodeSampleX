package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestSymbolPageContextFirstMatrix(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/npm/axios/1.12.0/axios.post")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()

	// Execution context is the leading row dimension (execution-context.md §6).
	mustContain(t, body, "node 22")
	mustContain(t, body, "TS 5.9 · pnpm · windows")
	mustContain(t, body, "safari 19")
	mustContain(t, body, "ELEVATED FAILURE")
	mustContain(t, body, "android-webview 140")
	mustContain(t, body, "UNKNOWN")
	mustContain(t, body, "no evidence")
	mustContain(t, body, "HIGH")

	// Context rows appear in snapshot order: node before safari before webview.
	iNode := strings.Index(body, "node 22")
	iSafari := strings.Index(body, "safari 19")
	iWebview := strings.Index(body, "android-webview 140")
	if !(iNode < iSafari && iSafari < iWebview) {
		t.Errorf("context rows out of order: node=%d safari=%d webview=%d", iNode, iSafari, iWebview)
	}

	// Evidence classes separated: 104 project observations, 7 verifications —
	// and never their sum.
	mustContain(t, body, ">104<")
	mustContain(t, body, ">7<")
	if strings.Contains(body, ">111<") {
		t.Error("PROJECT_* observations were summed with CONTRACT evidence")
	}

	// Failure cluster: facts labeled observed, causes labeled hypotheses.
	mustContain(t, body, "ERR_REQUIRE_ESM")
	mustContain(t, body, "observed")
	mustContain(t, body, "hypotheses")
	mustContain(t, body, "CONFIGURATION")
	mustContain(t, body, "72%")
	mustContain(t, body, "regression candidate")

	// SEO: descriptive title + breadcrumbs.
	mustContain(t, body, "<title>axios.post axios 1.12.0 node 22 compatibility — CodeSampleX</title>")
	mustContain(t, body, `"BreadcrumbList"`)
	mustContain(t, body, `rel="canonical" href="https://codesamplex.dev/npm/axios/1.12.0/axios.post"`)
}

func TestSymbolPageGolangMultiSegment(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/golang/github.com/a/b/v1.2.0/pkg.Func")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "go 1.26")
	mustContain(t, body, "MEDIUM")
	mustContain(t, body, "github.com/a/b")
}

func TestPackagePage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/npm/axios")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `href="/npm/axios/1.12.0"`)
	mustContain(t, body, `href="/npm/axios/1.11.0"`)
	mustContain(t, body, "ERR_REQUIRE_ESM")
	mustContain(t, body, "regression candidate")
}

// /explore is retained as a legacy redirect, not as an internal destination.
// Advertising it in both the visible breadcrumb and its JSON-LD made crawlers
// rediscover a URL that can only ever appear as "Page with redirect".
func TestPackagePageDoesNotAdvertiseLegacyExploreURL(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/npm/axios?lang=de").Body.String()

	mustContain(t, body, `href="/records?lang=de"`)
	mustContain(t, body, `https://codesamplex.dev/records?eco=npm`)
	if strings.Contains(body, "/explore") {
		t.Error("package page still advertises the legacy /explore redirect")
	}
}

func TestVersionPageListsSymbols(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/npm/axios/1.12.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, `href="/npm/axios/1.12.0/axios.post"`)
	mustContain(t, body, `href="/npm/axios/1.12.0/axios.get"`)
}

func TestVersionPageUsesQueryLinksForPathUnsafeSymbols(t *testing.T) {
	mux, store := newTestMux(t, nil)
	const (
		eco     = "gem"
		name    = "ruby-api-shapes"
		version = "1.0.0"
	)
	store.versions[eco+"|"+name] = []string{version}
	store.symbols[eco+"|"+name+"|"+version] = []string{
		"OpenStruct#[]", "Set#include?", "Namespace/member",
	}
	for _, symbol := range store.symbols[eco+"|"+name+"|"+version] {
		store.snapshots[snapKey("pkg:gem/"+name+"@"+version, symbol)] = `{
		  "schemaVersion":1,"purl":"pkg:gem/ruby-api-shapes@1.0.0",
		  "symbol":` + strconv.Quote(symbol) + `,"rows":[],"failures":[]
		}`
	}

	body := get(t, mux, "/gem/ruby-api-shapes/1.0.0").Body.String()
	mustContain(t, body, `href="/gem/ruby-api-shapes/1.0.0?symbol=OpenStruct%23%5B%5D"`)
	mustContain(t, body, `href="/gem/ruby-api-shapes/1.0.0?symbol=Set%23include%3F"`)
	mustContain(t, body, `href="/gem/ruby-api-shapes/1.0.0?symbol=Namespace%2Fmember"`)

	for _, encoded := range []string{
		"OpenStruct%23%5B%5D", "Set%23include%3F", "Namespace%2Fmember",
	} {
		rec := get(t, mux, "/gem/ruby-api-shapes/1.0.0?symbol="+encoded)
		if rec.Code != http.StatusOK {
			t.Errorf("symbol=%s status = %d, want 200", encoded, rec.Code)
		}
		mustContain(t, rec.Body.String(), `rel="canonical" href="https://codesamplex.dev/gem/ruby-api-shapes/1.0.0?symbol=`+encoded+`"`)
	}

	// Established links for ordinary symbols stay readable and routable.
	if got := symbolHref("npm", "axios", "1.12.0", "axios.post"); got != "/npm/axios/1.12.0/axios.post" {
		t.Fatalf("simple symbol href = %q", got)
	}
}

func TestGoSemanticImportVersionBaseRoute(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["golang|github.com/go-chi/chi/v5"] = []string{"v5.2.3"}
	rec := get(t, mux, "/golang/github.com/go-chi/chi/v5")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	mustContain(t, rec.Body.String(), `href="/golang/github.com/go-chi/chi/v5/v5.2.3"`)
}

func TestUnknownEcosystemAndPackage404(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	if rec := get(t, mux, "/gems/rails"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown ecosystem status %d", rec.Code)
	}
	if rec := get(t, mux, "/npm/no-such-package"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown package status %d", rec.Code)
	}
	if rec := get(t, mux, "/npm/axios/9.9.9/none.sym"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown symbol status %d", rec.Code)
	}
}

func TestRecordsSearchAndList(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/records?q=axios").Body.String()
	mustContain(t, body, `href="/npm/axios"`)

	all := get(t, mux, "/records").Body.String()
	mustContain(t, all, `href="/npm/axios"`)
	mustContain(t, all, `href="/golang/github.com/a/b"`)

	none := get(t, mux, "/records?q=zzzznothing").Body.String()
	mustContain(t, none, "No packages found.")

	// The old URL keeps working, query intact.
	rec := get(t, mux, "/explore?q=axios")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("/explore status = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/records?q=axios" {
		t.Errorf("Location = %q, want /records?q=axios", loc)
	}
}

// TestRecordsPagination pins the behavior a growing record needs: a page
// slice, an honest "x–y of N" line, working prev/next, and a stale page
// number that lands on the last real page instead of an empty screen.
func TestRecordsPagination(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.packages = nil
	for i := 0; i < recordsPerPage+5; i++ {
		store.packages = append(store.packages, PackageHit{
			Ecosystem: "npm", Name: fmt.Sprintf("pkg-%03d", i), LatestVersion: "1.0.0",
			EvidenceCount: int64(i + 1),
		})
	}

	first := get(t, mux, "/records").Body.String()
	mustContain(t, first, "1–40 of 45")
	mustContain(t, first, `href="/records?page=2"`)
	if strings.Contains(first, "pkg-040") {
		t.Error("page 1 spilled past its slice")
	}

	second := get(t, mux, "/records?page=2").Body.String()
	mustContain(t, second, "41–45 of 45")
	mustContain(t, second, "pkg-044")
	mustContain(t, second, `href="/records"`) // prev returns to page 1

	// A page beyond the end redirects to the last page rather than 404ing
	// or rendering nothing.
	rec := get(t, mux, "/records?page=99")
	if rec.Code != http.StatusFound {
		t.Fatalf("page=99 status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/records?page=2" {
		t.Errorf("Location = %q, want /records?page=2", loc)
	}
}

func TestSamplePage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/samples/sha256:d1e2f3")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "POST JSON with axios and retries")
	mustContain(t, body, `class="dim mono small sample-id"`)
	mustContain(t, body, "responds 200")
	mustContain(t, body, "retries on ECONNRESET")
	mustContain(t, body, "src/index.mjs")
	mustContain(t, body, "test/contract.mjs")
	mustContain(t, body, "Origin Seeder")
	mustContain(t, body, `href="/seeders/alice"`)
	mustContain(t, body, "CROSS_PASS")
	mustContain(t, body, "L4_CROSS_PASS")
	mustContain(t, body, "Verification level")
	mustContain(t, body, "Verification-run environments")
	mustContain(t, body, "Download the source artifact")
	mustContain(t, body, "MIT-0")
	// Receipt details: env context + capability, honestly labeled.
	mustContain(t, body, "CONTAINER_RUN")
	mustContain(t, body, "node 22.18")

	if rec := get(t, mux, "/samples/sha256:unknown"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown sample status %d", rec.Code)
	}
}

func TestSampleBadgeHelpIsAccessibleAndLocalized(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	body := get(t, mux, "/samples/sha256:d1e2f3").Body.String()

	if got := strings.Count(body, `data-badge-help aria-expanded="false"`); got != 2 {
		t.Fatalf("sample detail badge help triggers = %d, want 2", got)
	}
	for _, want := range []string{
		`aria-controls="sample-status-help" aria-describedby="sample-status-help"`,
		`aria-controls="sample-level-help" aria-describedby="sample-level-help"`,
		`id="sample-status-help" role="tooltip"`,
		`id="sample-level-help" role="tooltip"`,
		"PUBLISHED is public and awaiting independent verification",
		"L1 resolved dependencies",
		"help.addEventListener('mouseenter'",
		"trigger.addEventListener('focus'",
		"document.addEventListener('click'",
		"if(event.key==='Escape')",
	} {
		mustContain(t, body, want)
	}

	ko := get(t, mux, "/samples/sha256:d1e2f3?lang=ko").Body.String()
	mustContain(t, ko, "PUBLISHED는 공개 후 독립 검증 대기")
	mustContain(t, ko, "L1은 의존성 해결")
}

func TestSeederPage(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/seeders/alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	mustContain(t, body, "alice")
	mustContain(t, body, "POST JSON with axios and retries")
	mustContain(t, body, `href="/samples/sha256:d1e2f3"`)
	mustContain(t, body, "CROSS_PASS")
	mustContain(t, body, `aria-controls="status-help-0" aria-describedby="status-help-0"`)
	mustContain(t, body, `id="status-help-0" role="tooltip"`)
	mustContain(t, body, "PUBLISHED is public and awaiting independent verification")
}

// TestAdaptersPathRedirects: the capability page folded into the front
// page's support rows. The machine-readable matrix is still published at
// GET /v1/adapters, which is what goal.md §13.1 requires.
func TestAdaptersPathRedirects(t *testing.T) {
	mux, _ := newTestMux(t, nil)
	rec := get(t, mux, "/adapters")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("status %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
}
