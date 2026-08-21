package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The dashboard is what an operator reads; the farm and its credentials are
// what they act on. Mixed into one column, reaching a token meant scrolling
// past six panels of numbers.
func TestDashboardSplitsReadingFromActing(t *testing.T) {
	mux, secret := configuredMux(t, &fakeStore{})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	req.SetBasicAuth("recuerdame", secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()

	for _, want := range []string{
		`id="tab-ops"`, `id="tab-farm"`,
		`aria-controls="tab-ops"`, `aria-controls="tab-farm"`,
		`role="tablist"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard is missing %q", want)
		}
	}

	ops := strings.Index(body, `id="tab-ops"`)
	farm := strings.Index(body, `id="tab-farm"`)
	if ops < 0 || farm < 0 || ops > farm {
		t.Fatalf("panels out of order: ops=%d farm=%d", ops, farm)
	}
	// The things an operator acts on belong together, behind one tab.
	inFarm := body[farm:]
	for _, want := range []string{"admin-token-title", "farm-title", "sample-authoring", "verify-worker"} {
		if !strings.Contains(inFarm, want) {
			t.Errorf("%q did not land in the farm tab", want)
		}
	}
	// And the reading surface must not have followed them.
	if strings.Contains(inFarm, "health-title") {
		t.Error("server health belongs on the dashboard, not beside the credentials")
	}
	// Exactly one panel starts visible, or the page opens showing both and
	// the split buys nothing until the script runs.
	opsTag := body[ops : strings.Index(body[ops:], ">")+ops]
	farmTag := body[farm : strings.Index(body[farm:], ">")+farm]
	if strings.Contains(opsTag, "hidden") {
		t.Error("the dashboard panel starts hidden; the page opens on nothing")
	}
	if !strings.Contains(farmTag, "hidden") {
		t.Error("both panels start visible, so the split does nothing before JS runs")
	}
}
