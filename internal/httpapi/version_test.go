package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
)

func productionBuild() buildinfo.Info {
	return buildinfo.Info{
		Version:     "v0.1.44-66",
		Revision:    "2a6af6a8d73f51e4c941908f76527bd9899437ce",
		Environment: "production",
		BuiltAt:     time.Date(2026, 8, 26, 0, 11, 2, 0, time.UTC),
	}
}

func getVersion(t *testing.T, d Deps) (*httptest.ResponseRecorder, versionResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	NewMux(d).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	var got versionResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec, got
}

// The deploy transaction asks this endpoint whether the commit it shipped is
// the commit now answering requests. A container environment variable only
// proves what was configured; this proves what started.
func TestVersionReportsTheRunningBuild(t *testing.T) {
	rec, got := getVersion(t, Deps{Build: productionBuild()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	want := versionResponse{
		Service:       "csx-server",
		Version:       "v0.1.44-66",
		Revision:      "2a6af6a8d73f51e4c941908f76527bd9899437ce",
		ShortRevision: "2a6af6a",
		Environment:   "production",
		BuiltAt:       "2026-08-26T00:11:02Z",
	}
	if got != want {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// A cached identity is a wrong identity the moment a rollout finishes, and
// the probe that reads it during a deploy is the one that must never be
// answered from a cache.
func TestVersionIsNeverCached(t *testing.T) {
	rec, _ := getVersion(t, Deps{Build: productionBuild()})
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// /healthz proves the database is reachable and is allowed to fail for that
// reason. This endpoint decides whether the right commit is serving, so it
// must answer with no store configured at all.
func TestVersionAnswersWithoutAStore(t *testing.T) {
	rec, got := getVersion(t, Deps{Build: productionBuild()})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d with no store", rec.Code)
	}
	if got.Revision != "2a6af6a8d73f51e4c941908f76527bd9899437ce" {
		t.Errorf("revision = %q", got.Revision)
	}

	health := httptest.NewRecorder()
	NewMux(Deps{}).ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code == http.StatusOK {
		t.Error("/healthz reported ok with no store; the two endpoints answer different questions")
	}
}

// An unstamped build must not describe itself as production, and must not
// invent a revision it does not have.
func TestVersionOfAnUnstampedBuild(t *testing.T) {
	_, got := getVersion(t, Deps{Build: buildinfo.Info{Environment: buildinfo.EnvDevelopment}})
	if got.Environment != "development" {
		t.Errorf("environment = %q", got.Environment)
	}
	if got.Revision != "" || got.ShortRevision != "" || got.Version != "" || got.BuiltAt != "" {
		t.Errorf("unstamped build reported an identity: %+v", got)
	}
	if got.Service != "csx-server" {
		t.Errorf("service = %q", got.Service)
	}
}

// The name is not "csx". The client a visitor downloads has its own release
// version on its own cadence, and conflating the two is what made the footer
// unreadable.
func TestVersionNamesTheServerNotTheClient(t *testing.T) {
	_, got := getVersion(t, Deps{Build: productionBuild()})
	if got.Service != "csx-server" {
		t.Errorf("service = %q, want csx-server", got.Service)
	}
}

func TestVersionRejectsWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	NewMux(Deps{Build: productionBuild()}).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/version", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("POST /version answered %d", rec.Code)
	}
}
