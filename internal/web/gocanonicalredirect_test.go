package web

import (
	"net/http"
	"testing"
)

// A bare Go version in a URL is redirected to the canonical one rather than
// 404'd.
//
// These URLs were real. The corpus stored `github.com/jackc/pgx/v5@5.10.0`
// beside `@v5.10.0` as two rows, so both had pages, both were reachable, and
// both could be linked or indexed. ParsePURL now repairs the spelling and
// migration 0030 folds the stored rows, which is right — and it would turn
// every bare URL already out there into a dead end.
//
// 301 rather than 404 because the bare URL is a real address for a real
// release, just spelled wrong, and the canonical one exists. A permanent
// redirect moves whatever authority the old URL accumulated onto the page that
// is now the only one.
func TestABareGoVersionURLRedirectsToTheCanonicalOne(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["golang|github.com/jackc/pgx/v5"] = []string{"v5.10.0"}

	for _, tc := range []struct{ from, want string }{
		{"/golang/github.com/jackc/pgx/v5/5.10.0",
			"/golang/github.com/jackc/pgx/v5/v5.10.0"},
		{"/golang/github.com/jackc/pgx/v5/5.10.0/pgx.Row.Scan",
			"/golang/github.com/jackc/pgx/v5/v5.10.0/pgx.Row.Scan"},
		{"/golang/github.com/jackc/pgx/v5/5.10.0?lang=ko",
			"/golang/github.com/jackc/pgx/v5/v5.10.0?lang=ko"},
	} {
		rec := get(t, mux, tc.from)
		if rec.Code != http.StatusMovedPermanently {
			t.Errorf("%s: status = %d, want 301", tc.from, rec.Code)
			continue
		}
		if loc := rec.Header().Get("Location"); loc != tc.want {
			t.Errorf("%s -> %q, want %q", tc.from, loc, tc.want)
		}
	}
}

// Only golang, and only a version segment that looks like a Go release missing
// its prefix. Every other ecosystem writes bare versions canonically, so a
// redirect there would send a reader away from the only correct URL.
func TestTheGoVersionRedirectDoesNotTouchOtherEcosystems(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["npm|axios"] = []string{"1.12.0"}
	store.versions["pypi|flask"] = []string{"3.0.0"}

	for _, path := range []string{
		"/npm/axios/1.12.0",
		"/pypi/flask/3.0.0",
	} {
		if rec := get(t, mux, path); rec.Code == http.StatusMovedPermanently {
			t.Errorf("%s was redirected to %q", path, rec.Header().Get("Location"))
		}
	}
}

// A canonical URL is served, not redirected — otherwise the redirect would
// loop on the page it points at.
func TestACanonicalGoVersionURLIsNotRedirected(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.versions["golang|github.com/jackc/pgx/v5"] = []string{"v5.10.0"}
	if rec := get(t, mux, "/golang/github.com/jackc/pgx/v5/v5.10.0"); rec.Code == http.StatusMovedPermanently {
		t.Errorf("the canonical URL redirected to %q", rec.Header().Get("Location"))
	}
}
