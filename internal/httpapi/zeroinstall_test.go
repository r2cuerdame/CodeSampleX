package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

// The zero-install surface (#318): every route the README and the features
// page publish as "readable without an account" must be readable from a
// page on another origin, or the "use from a browser" mark in the matrix is
// a lie for that route. A fetch from a script or a cloud agent never looks
// at these headers; a browser refuses the response without them.
func TestEveryPublishedReadRouteIsOpenToAnyOrigin(t *testing.T) {
	srv, store, _ := newTestServer(t, func(d *Deps) { d.Cfg.PublicURL = "https://csx.example" })
	seedFootprintSample(t, store)

	open := []struct{ method, path string }{
		{"GET", "/v1/stats"},
		{"GET", "/v1/adapters"},
		{"GET", "/v1/wanted"},
		{"GET", "/v1/samples/" + footprintSample},
		{"GET", "/v1/registry/packages/pkg%3Anpm%2Faxios%401.12.0"},
		{"GET", "/v1/registry/symbols/npm/axios/axios.post"},
		{"GET", "/v2/search?q=left+pad"},
		{"POST", "/v2/search"},
		{"POST", "/v1/footprints/execution"},
		{"GET", "/version"},
	}
	for _, route := range open {
		req, err := http.NewRequest(route.method, srv.URL+route.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://agent.example")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		// The status is not the point here (an empty body is a 400 on the
		// POST routes); the header must be on the refusal as well as on the
		// answer, or a page cannot even read the sentence that refused it.
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s %s: Access-Control-Allow-Origin = %q, want *", route.method, route.path, got)
		}
	}
}

// A browser sends OPTIONS before a POST with a JSON body. Only the two
// zero-install POST routes answer it; every other POST expects a client.
func TestTheZeroInstallPostRoutesAnswerPreflight(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	for _, path := range []string{"/v2/search", "/v1/footprints/execution"} {
		req, err := http.NewRequest(http.MethodOptions, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://agent.example")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("OPTIONS %s = %d, want 204", path, resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Access-Control-Allow-Methods"), "POST") {
			t.Errorf("OPTIONS %s does not allow POST: %q", path, resp.Header.Get("Access-Control-Allow-Methods"))
		}
		if !strings.Contains(strings.ToLower(resp.Header.Get("Access-Control-Allow-Headers")), "content-type") {
			t.Errorf("OPTIONS %s does not allow Content-Type: %q", path, resp.Header.Get("Access-Control-Allow-Headers"))
		}
	}
}

// The other half: a route that carries a token or a seeder identity is not
// opened. A wildcard origin on those would invite a page to send them.
func TestCredentialedRoutesAreNotOpenedToOtherOrigins(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	for _, route := range []struct{ method, path string }{
		{"POST", "/v1/evidence/batches"},
		{"POST", "/v1/samples"},
		{"POST", "/v1/adoptions"},
		{"POST", "/v1/verifications"},
		{"POST", "/v1/peers/announce"},
		{"POST", "/v1/auth/github/device"},
		{"OPTIONS", "/v1/samples"},
	} {
		req, err := http.NewRequest(route.method, srv.URL+route.path, strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", "https://agent.example")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s %s: Access-Control-Allow-Origin = %q on a credentialed route", route.method, route.path, got)
		}
	}
}
