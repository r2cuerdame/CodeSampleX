package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"
)

func newGoNetStore() *fakeStore {
	f := newFakeStore()
	f.versions["golang|golang.org/x/net"] = []string{"v0.51.0", "v0.50.0"}
	f.symbols["golang|golang.org/x/net|v0.51.0"] = []string{"golang.org/x/net/html.ErrorToken"}
	purl := "pkg:golang/golang.org/x/net@v0.51.0"
	f.snapshots[snapKey(purl, "")] = cubeSnap(purl, "", "ubuntu", "x64",
		"go", "1.26", "go", "PROJECT_COMPILE", 1, 0)
	f.snapshots[snapKey(purl, "golang.org/x/net/html.ErrorToken")] = cubeSnap(purl,
		"golang.org/x/net/html.ErrorToken", "ubuntu", "x64", "go", "1.26", "go", "CONTRACT", 1, 0)
	return f
}

func TestGoNetPackageRouteReturns200AndExcludesForeignVersions(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newGoNetStore() })

	res := get(t, mux, "/golang/golang.org/x/net")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "v0.51.0") {
		t.Fatalf("expected version v0.51.0 in body: %s", body)
	}
	if strings.Contains(body, "v0.47.0") {
		t.Fatalf("unexpected foreign version v0.47.0 in body: %s", body)
	}
}

func TestGoNetFilteredCubeURLReturns200WithKoreanLocale(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newGoNetStore() })

	target := "/golang/golang.org/x/net?f_runtime=go+1.26&f_symbol=golang.org%2Fx%2Fnet%2Fhtml.ErrorToken&lang=ko#cube"
	res := get(t, mux, target)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if !strings.Contains(body, "html.ErrorToken") {
		t.Fatalf("expected symbol in body: %s", body)
	}
}

func TestGoNetNonexistentVersionDeepLinkReturns404(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newGoNetStore() })

	res := get(t, mux, "/golang/golang.org/x/net/v0.47.0")
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", res.Code, res.Body.String())
	}
}

func TestGoNetValidVersionDeepLinkReturns200(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = newGoNetStore() })

	res := get(t, mux, "/golang/golang.org/x/net/v0.51.0")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", res.Code, res.Body.String())
	}
}

func TestGoNetConcurrentRepresentativeReads(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) {
		d.Store = newGoNetStore()
	})

	urls := []string{
		"/golang/golang.org/x/net",
		"/golang/golang.org/x/net?f_runtime=go+1.26&f_symbol=golang.org%2Fx%2Fnet%2Fhtml.ErrorToken&lang=ko#cube",
		"/golang/golang.org/x/net/v0.51.0",
	}

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		for _, u := range urls {
			wg.Add(1)
			go func(url string) {
				defer wg.Done()
				res := get(t, mux, url)
				if res.Code != http.StatusOK {
					t.Errorf("GET %s status = %d, want 200", url, res.Code)
				}
			}(u)
		}
	}
	wg.Wait()
}
