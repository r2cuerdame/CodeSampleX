package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The product-defect channel had no way to close anything.
//
// A report arrives, the server marks it "no replay lane: a person triages it,
// which is why it is not left pending", and then there was no route by which a
// person could record that they had. The store has held SetCSXIssueVerdict and
// LinkCSXIssueCanonical since the channel shipped; nothing exposed them.
//
// Production, 2026-08-30: 18 reports, every one of them with an empty verdict,
// the oldest four days old. A channel that only accumulates teaches its
// reporters that reporting does nothing.
func TestAnOperatorCanCloseAProductDefectReport(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	row, _, err := store.RecordCSXIssueReport(ctx, serverstore.CSXIssueReportRow{
		Fingerprint: "fp-close-me",
		Surface:     domain.CSXSurfaceMCP,
		IssueKind:   "runtime-behavior",
		Component:   "run_observed_command",
		ReportJSON:  `{"actualBehavior":"a","expectedBehavior":"b"}`,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	mux, _ := newIssueTriageMux(t, store)

	res := postAdminJSON(t, mux, "/admin/api/csx-issues/verdict",
		map[string]any{"id": row.ID, "verdict": domain.CSXIssueVerdictDefect})
	if res.Code != http.StatusOK {
		t.Fatalf("verdict POST = %d: %s", res.Code, res.Body.String())
	}

	got, ok, err := store.CSXIssueReportByID(ctx, row.ID)
	if err != nil || !ok {
		t.Fatalf("report gone: ok=%v err=%v", ok, err)
	}
	if got.Verdict == "" {
		t.Error("the report is still open after an operator closed it")
	}
	if got.VerdictAt.IsZero() {
		t.Error("the verdict carries no time, so nobody can tell when it was decided")
	}
}

// A confirmed defect becomes a bug somebody can follow. The link is what turns
// a repeat report into an answer instead of another row.
func TestAConfirmedDefectCanBeLinkedToItsBug(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	row, _, err := store.RecordCSXIssueReport(ctx, serverstore.CSXIssueReportRow{
		Fingerprint: "fp-link-me",
		Surface:     domain.CSXSurfaceVerifier,
		IssueKind:   "runtime-behavior",
		Component:   "csx sample verify",
		ReportJSON:  `{"actualBehavior":"a","expectedBehavior":"b"}`,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := newIssueTriageMux(t, store)

	// Only a confirmed defect may be linked — the store enforces it, and the
	// route must not be a way around that.
	early := postAdminJSON(t, mux, "/admin/api/csx-issues/canonical",
		map[string]any{"id": row.ID, "ref": "GH-999"})
	if early.Code == http.StatusOK {
		var body struct {
			Linked bool `json:"linked"`
		}
		_ = json.Unmarshal(early.Body.Bytes(), &body)
		if body.Linked {
			t.Error("an untriaged report was linked to a bug, which the store forbids")
		}
	}

	if res := postAdminJSON(t, mux, "/admin/api/csx-issues/verdict",
		map[string]any{"id": row.ID, "verdict": domain.CSXIssueVerdictDefect}); res.Code != http.StatusOK {
		t.Fatalf("verdict POST = %d: %s", res.Code, res.Body.String())
	}
	if res := postAdminJSON(t, mux, "/admin/api/csx-issues/canonical",
		map[string]any{"id": row.ID, "ref": "GH-999"}); res.Code != http.StatusOK {
		t.Fatalf("canonical POST = %d: %s", res.Code, res.Body.String())
	}

	got, _, err := store.CSXIssueReportByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalRef != "GH-999" {
		t.Errorf("canonicalRef = %q, want the bug a repeat reporter should be handed", got.CanonicalRef)
	}
}

// A verdict is a decision, not a draft. Overwriting one would let a later
// operator quietly reverse an earlier one with no trace.
func TestAVerdictIsNotOverwritten(t *testing.T) {
	store := serverstore.NewFake()
	ctx := context.Background()
	row, _, err := store.RecordCSXIssueReport(ctx, serverstore.CSXIssueReportRow{
		Fingerprint: "fp-once",
		Surface:     domain.CSXSurfaceMCP,
		IssueKind:   "runtime-behavior",
		Component:   "search_known_solution",
		ReportJSON:  `{"actualBehavior":"a","expectedBehavior":"b"}`,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := newIssueTriageMux(t, store)

	first := postAdminJSON(t, mux, "/admin/api/csx-issues/verdict",
		map[string]any{"id": row.ID, "verdict": domain.CSXIssueVerdictDefect})
	if first.Code != http.StatusOK {
		t.Fatalf("first verdict = %d: %s", first.Code, first.Body.String())
	}
	_ = postAdminJSON(t, mux, "/admin/api/csx-issues/verdict",
		map[string]any{"id": row.ID, "verdict": domain.CSXIssueVerdictExpectedBehavior})

	got, _, err := store.CSXIssueReportByID(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != domain.CSXIssueVerdictDefect {
		t.Errorf("verdict = %q, want the first decision kept", got.Verdict)
	}
}

// The channel is a private operator surface, not something a reporter can
// drive. A defect a reporter could close is a defect a reporter could hide.
func TestClosingAReportNeedsAdminCredentials(t *testing.T) {
	store := serverstore.NewFake()
	mux, _ := newIssueTriageMux(t, store)

	req := httptest.NewRequest(http.MethodPost, "/admin/api/csx-issues/verdict",
		strings.NewReader(`{"id":1,"verdict":"confirmed"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)

	if res.Code == http.StatusOK {
		t.Errorf("an unauthenticated caller closed a report: %d", res.Code)
	}
}

// newIssueTriageMux wires the admin surface with a real store behind the
// product-defect channel, so these tests exercise the route and the store
// contract together rather than a handler talking to a double.
func newIssueTriageMux(t *testing.T, store *serverstore.Fake) (*http.ServeMux, string) {
	t.Helper()
	return configuredMuxWithChannels(t, &fakeStore{}, nil, store)
}

func postAdminJSON(t *testing.T, mux *http.ServeMux, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	// The mutation guard: same-origin, an explicit CSRF marker and a JSON body.
	// A browser cannot forge all three from another site, which is what keeps
	// a panel action from being triggerable by a page the operator visits.
	req.Header.Set("Origin", "https://codesamplex.dev")
	req.Header.Set("X-CSX-CSRF", "1")
	req.SetBasicAuth("recuerdame", "a-long-random-admin-secret")
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	return res
}
