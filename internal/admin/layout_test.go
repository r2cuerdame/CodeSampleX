package admin

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func adminBody(t *testing.T) string {
	t.Helper()
	mux, secret := configuredMux(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("recuerdame", secret)
	req.RemoteAddr = "198.51.100.8:443"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The page carried a dozen sections at one height, and the operator scrolled
// past read-only charts to reach the two controls they came for. The sections
// a reader only glances at fold away; the ones they act on do not.
//
// Nothing is deleted: a detail behind a disclosure is still in the document a
// crawler or a Ctrl-F sees. Several sections are conditional on the store
// having data, so each is judged only when it is on the page at all.
func TestAdminFoldsTheReadOnlySectionsAndLeavesTheControlsOpen(t *testing.T) {
	body := adminBody(t)

	folded := regexp.MustCompile(`(?s)<details[^>]*class="[^"]*metrics[^"]*"[^>]*>.*?</details>`).FindString(body)
	if folded == "" {
		t.Fatal("the read-only sections are not behind a disclosure")
	}
	if regexp.MustCompile(`<details[^>]*\sopen`).MatchString(folded) {
		t.Error("the folded block starts open, which is the layout it replaced")
	}

	glanced := []string{"velocity-title", "quality-title", "verification-title",
		"ecosystem-title", "adoption-title", "source-title", "jobs-title"}
	foldedAny := false
	for _, id := range glanced {
		if !strings.Contains(body, id) {
			continue // conditional on store data this fixture has none of
		}
		if !strings.Contains(folded, id) {
			t.Errorf("%s is read-only but was left outside the fold", id)
			continue
		}
		foldedAny = true
	}
	if !foldedAny {
		t.Fatal("nothing was folded, so this asserts nothing")
	}

	// The summary panel leads with "지금 개입할 것"; it is the glance that
	// decides whether to act, so it stays open with the controls.
	acted := []string{"summary-title", "sample-authoring-title", "admin-token-title", "health-title"}
	for _, id := range acted {
		if !strings.Contains(body, id) {
			t.Errorf("%s vanished from the page entirely", id)
			continue
		}
		if strings.Contains(folded, id) {
			t.Errorf("%s was folded away; it is something the operator acts on", id)
		}
	}
}

// Folding must not cost the page a section it used to render.
func TestAdminFoldKeepsEverySection(t *testing.T) {
	body := adminBody(t)
	for _, id := range []string{
		"sample-authoring-title", "admin-token-title", "wanted-title", "health-title",
	} {
		if !strings.Contains(body, id) {
			t.Errorf("section %s disappeared", id)
		}
	}
}

// Three sections were measuring nothing an operator acts on.
//
// 검색 효용 rendered a hit rate from search_outcomes_daily, which holds zero
// rows in production — a headline product metric occupying the page while
// measuring nothing. 생태계 구성 and 채택 성과 restate a corpus-wide
// distribution that does not move in a day. Meanwhile the three failures that
// actually happened — a third of the corpus duplicated, a farm running at half
// its worker count, coordinates locked for a day — appeared nowhere. Space on
// this page is not free; it is the thing the operator scrolls past.
func TestAdminDropsTheSectionsThatMeasuredNothing(t *testing.T) {
	body := adminBody(t)
	for _, gone := range []string{"quality-title", "ecosystem-title", "adoption-title"} {
		if strings.Contains(body, gone) {
			t.Errorf("%s is still on the page", gone)
		}
	}
	// The summary panel keeps its own No-match figure, so the underlying
	// measurement is not what was dropped — only the panel restating it.
	if !strings.Contains(body, "summary-title") {
		t.Error("the summary panel went with them")
	}
}

// The operator's own name for a machine is not the machine's name for itself.
// This field was removed as a duplicate of the self-reported computer_name;
// that was wrong. A worker reports EC2AMAZ-G9R4PRD, which is a hostname nobody
// wants to read, and the linux farm reports nothing at all on the CLI it is
// running. The names actually in use were 회사-01 and 집-02 — typed, because
// they say where the machine is, which is what the operator is tracking.
func TestSampleWorkerFormTakesAnOperatorName(t *testing.T) {
	body := adminBody(t)
	form := regexp.MustCompile(`(?s)<form[^>]*id="sample-worker-form".*?</form>`).FindString(body)
	if form == "" {
		t.Fatal("the sample worker form is gone")
	}
	if !strings.Contains(form, `name="label"`) {
		t.Errorf("the sample worker form has no name field: %s", form)
	}
	// Still optional: an omitted name falls back to the model, which is what
	// lets a provisioning script issue workers without inventing one.
	if regexp.MustCompile(`name="label"[^>]*required`).MatchString(form) {
		t.Error("the name field is required again; blank must still fall back to the model")
	}
}

// The draft inbox restated what the farm panel already reports per worker,
// one row per draft, on a page whose problem was length.
func TestAdminDropsTheDraftInbox(t *testing.T) {
	body := adminBody(t)
	for _, gone := range []string{"sample-worker-drafts", "샘플 검증 대기함"} {
		if strings.Contains(body, gone) {
			t.Errorf("the draft inbox is still on the page: %q", gone)
		}
	}
}
