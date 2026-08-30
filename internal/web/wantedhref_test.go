package web

import (
	"strings"
	"testing"
)

// Every wanted row advertises a release and then links as if it did not.
//
// The label reads "golang/github.com/jackc/pgx/v5@v5.10.0" and the href was
// pkgHref(eco, name), which drops the version — so rows asking about different
// releases of one package all resolve to the same destination. On production
// 72 rows collapsed onto 60 URLs, and a reader who clicked the release they
// were interested in got the package instead, with nothing saying so.
func TestAWantedRowLinksToTheReleaseItAdvertises(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.wanted = []WantedRow{
		{Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Scene", Asks: 3},
		{Ecosystem: "npm", Name: "three", Version: "0.179.0", Symbol: "Mesh", Asks: 2},
	}

	body := get(t, mux, "/wanted").Body.String()
	for _, want := range []string{"/npm/three/0.180.0", "/npm/three/0.179.0"} {
		if !strings.Contains(body, `href="`+want+`"`) {
			t.Errorf("no row links to %s — two releases share one destination", want)
		}
	}
}
