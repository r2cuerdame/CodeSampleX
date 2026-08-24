package web

import (
	"html"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestFailureClusterRendersTerminationSummaryQualityAndDiagnosticCandidate(t *testing.T) {
	views := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", TerminationKind: string(domain.TerminationTimeout), TimeoutMillis: 600000,
		ErrorSummary: "go test timeout after <duration>", Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Count: 147, EvidenceQuality: string(domain.EvidenceComplete), DiagnosticCandidate: false,
		EnvVariants: []domain.FailureEnvironmentVariant{{Summary: map[string]string{"os": "windows", "runtime": "go@1.26"}, Count: 147}},
	}})
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	got := views[0]
	if got.Termination != "timeout 10m" || got.ErrorSummary != "go test timeout after <duration>" || got.EvidenceQuality != "complete" {
		t.Fatalf("view = %+v", got)
	}
}

func TestEvidenceGapNeverRendersRawNoErrorCodeCopy(t *testing.T) {
	views := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", Count: 227, EvidenceQuality: string(domain.EvidenceLegacyIncomplete), DiagnosticCandidate: true,
	}})
	if len(views) != 1 || !views[0].EvidenceGap || !views[0].DiagnosticCandidate {
		t.Fatalf("gap view = %+v", views)
	}
	b, err := os.ReadFile("templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, forbidden := range []string{"no error code recorded", "에러 코드 기록 없음"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("raw no-error-code copy survived: %q", forbidden)
		}
	}
	for _, want := range []string{"symbol.evidence_gap", "symbol.evidence_quality", "symbol.diagnostic_candidate"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q: %s", want, page)
		}
	}
}

// The storage cap is 512 bytes, which is a paragraph, and the cluster row is
// meant to be readable at a glance: "TEST · exit 1 · connection refused".
// Production's first modern cluster arrived as a whole Go test failure block
// joined with " · " and the page printed all of it, cut mid-word at the byte
// cap. The row leads with as much as reads as a line; the stored text stays
// reachable in full.
func TestALongNormalizedErrorLeadsWithALineAndKeepsTheRestReachable(t *testing.T) {
	first := "--- FAIL: TestRebuildingOverPreservedLegacyRowsDoesNotDoubleTheClusterLedger (0.00s)"
	full := first + " · clusterledger_test.go:<n>: a preserved pre-contract fingerprint is being served as a current cluster" +
		" · cluster-observation ledger = <n> over <n> rows, want <n>"
	views := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", TerminationKind: string(domain.TerminationExit), ExitCode: intPtr(1),
		ErrorSummary: full, Fingerprint: "sha256:" + strings.Repeat("7", 64),
		Count: 1, EvidenceQuality: string(domain.EvidenceComplete),
	}})
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	got := views[0]
	if len(got.ErrorSummary) > clusterErrorSummaryDisplayBytes {
		t.Errorf("displayed summary is %d bytes, over the %d-byte line budget: %q",
			len(got.ErrorSummary), clusterErrorSummaryDisplayBytes, got.ErrorSummary)
	}
	if !strings.HasPrefix(got.ErrorSummary, first) {
		t.Errorf("displayed summary does not lead with the failure line: %q", got.ErrorSummary)
	}
	if got.ErrorSummaryFull != full {
		t.Errorf("full normalized error was not preserved for the title: %q", got.ErrorSummaryFull)
	}
	// A summary that already reads as a line is shown as it is, with no
	// title duplicating it.
	short := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", ErrorSummary: "connection refused <ip>:<port>",
		Count: 3, EvidenceQuality: string(domain.EvidenceComplete),
	}})[0]
	if short.ErrorSummary != "connection refused <ip>:<port>" || short.ErrorSummaryFull != "" {
		t.Errorf("a short summary was clamped or duplicated: %+v", short)
	}
}

func intPtr(v int) *int { return &v }

// The same clamp, through the real template on the real route: what a reader
// actually gets is one line plus a reachable full text, not a paragraph cut
// mid-word.
func TestSymbolPageRendersALongNormalizedErrorAsALine(t *testing.T) {
	mux, store := newTestMux(t, nil)
	full := "--- FAIL: TestSomethingRatherLongIndeed (0.00s) · " +
		"ledger_test.go:<n>: a preserved pre-contract fingerprint is being served as a current cluster · " +
		"cluster-observation ledger = <n> over <n> rows, want <n>"
	store.clusters["npm|axios"] = []string{`{
		"symbol":"axios.post","stage":"PROJECT_TEST","terminationKind":"exit","exitCode":1,
		"errorSummary":` + strconv.Quote(full) + `,
		"evidenceQuality":"complete","count":1,"observationCount":1,
		"fingerprint":"sha256:` + strings.Repeat("7", 64) + `",
		"envSummary":{"os":"windows","runtime":"go@1.26"},"versions":["1.12.0"]}`}

	body := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()
	// The title may carry the whole text; the row itself may not.
	if strings.Contains(body, ">"+html.EscapeString(full)+"</div>") {
		t.Error("the whole 512-byte-class summary was printed into the cluster row")
	}
	mustContain(t, body, "--- FAIL: TestSomethingRatherLongIndeed (0.00s)")
	mustContain(t, body, `title="`+html.EscapeString(full)+`"`)
	// The structured coordinates a reader needs beside it survive.
	for _, want := range []string{"exit 1", "os=windows", "runtime=go@1.26"} {
		mustContain(t, body, want)
	}
}
