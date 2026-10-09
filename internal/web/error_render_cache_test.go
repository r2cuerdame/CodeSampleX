package web

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type farm202DiscardResponse struct{ h http.Header }

func (w *farm202DiscardResponse) Header() http.Header       { return w.h }
func (*farm202DiscardResponse) WriteHeader(int)             {}
func (*farm202DiscardResponse) Write(p []byte) (int, error) { return io.Discard.Write(p) }
func TestFarm202RepeatedUnavailableDoesNotRerenderChrome(t *testing.T) {
	s := &site{tmpl: parseTemplates()}
	data := errorPage{basePage: basePage{Lang: "ko", Title: "잠시 이용할 수 없습니다", Description: "잠시 이용할 수 없습니다", Canonical: "https://codesamplex.dev/npm/x", NoIndex: true, path: "/npm/x"}, Status: 503}
	w := &farm202DiscardResponse{h: make(http.Header)}
	s.render(w, "error", 503, data)
	n := testing.AllocsPerRun(30, func() { s.render(w, "error", 503, data) })
	t.Logf("unchanged unavailable response allocations: %.0f", n)
	if n > 80 {
		t.Fatalf("unchanged error response repeated template execution: %.0f allocations, budget 80", n)
	}
}

func TestFarm202RenderedErrorsMatchCurrentTemplateInputs(t *testing.T) {
	s := &site{d: Deps{PublicURL: "https://codesamplex.dev"}, tmpl: parseTemplates()}
	for _, lang := range []string{"en", "ko", "ja", "fr", "es", "de", "pt-BR", "ru", "zh-CN"} {
		for _, status := range []int{404, 503, 504} {
			for _, target := range []string{"/npm/one?lang=ko&pin=a%26b", "/gaps?search=%3Cscript%3E", "/npm/two?pin=a&pin=b"} {
				r := httptest.NewRequest("GET", target, nil)
				b := s.page(r, lang, "<title> & current", "<description> & current")
				b.Alternates = nil
				b.NoIndex = true
				if status == 404 {
					b.Canonical = ""
				}
				data := errorPage{basePage: b, Status: status}
				var expected bytes.Buffer
				if err := s.tmpl["error"].ExecuteTemplate(&expected, "base.html", data); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					w := httptest.NewRecorder()
					s.render(w, "error", status, data)
					if w.Code != status || w.Body.String() != expected.String() {
						t.Fatalf("response differs for %s %d %s", lang, status, target)
					}
				}
			}
		}
	}
}
func TestFarm202ErrorRenderCacheIsBoundedAndOversizedIsComplete(t *testing.T) {
	s := &site{tmpl: parseTemplates()}
	d := errorPage{basePage: basePage{Lang: "en", Title: "current", NoIndex: true, path: "/npm/x"}, Status: 503}
	var expected bytes.Buffer
	if err := s.tmpl["error"].ExecuteTemplate(&expected, "base.html", d); err != nil {
		t.Fatal(err)
	}
	s.errorRenderCache.maxBytes = len(expected.String())*4 + 4096
	s.errorRenderCache.maxEntries = 2
	for i := range 12 {
		d.Canonical = fmt.Sprintf("https://codesamplex.dev/npm/%d", i)
		w := httptest.NewRecorder()
		s.render(w, "error", 503, d)
		if w.Body.Len() == 0 {
			t.Fatal("empty current response")
		}
		if len(s.errorRenderCache.entries) > 2 || s.errorRenderCache.bytes > s.errorRenderCache.maxBytes {
			t.Fatal("unbounded rendered error retention")
		}
	}
	s.errorRenderCache = errorRenderCache{maxBytes: 512, maxEntries: 2}
	d.Canonical = ""
	for range 2 {
		w := httptest.NewRecorder()
		s.render(w, "error", 503, d)
		if w.Body.String() != expected.String() {
			t.Fatal("oversized response was truncated")
		}
	}
	if len(s.errorRenderCache.entries) != 0 || s.errorRenderCache.bytes != 0 {
		t.Fatal("oversized error was retained")
	}
}
func TestFarm202ConcurrentErrorRenderingKeepsRequestInputs(t *testing.T) {
	s := &site{d: Deps{PublicURL: "https://codesamplex.dev"}, tmpl: parseTemplates()}
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := fmt.Sprintf("/npm/x?pin=%d&lang=ko", i%4)
			r := httptest.NewRequest("GET", path, nil)
			b := s.page(r, "ko", "current", "current")
			b.NoIndex = true
			b.Alternates = nil
			d := errorPage{basePage: b, Status: 503}
			var expected bytes.Buffer
			if err := s.tmpl["error"].ExecuteTemplate(&expected, "base.html", d); err != nil {
				t.Error(err)
				return
			}
			w := httptest.NewRecorder()
			s.render(w, "error", 503, d)
			if w.Body.String() != expected.String() {
				t.Errorf("another request's error body at pin %d", i%4)
			}
		}(i)
	}
	wg.Wait()
}
func TestFarm202UnavailableStillSetsCurrentStatusAndRetryHeaders(t *testing.T) {
	s := &site{d: Deps{PublicURL: "https://codesamplex.dev"}, tmpl: parseTemplates()}
	for _, status := range []int{503, 504} {
		for range 2 {
			r := httptest.NewRequest("GET", "/npm/x?lang=ko&pin=a%26b", nil)
			w := httptest.NewRecorder()
			s.unavailableWithStatus(w, r, "ko", status)
			if w.Code != status || w.Header().Get("Retry-After") != "2" || w.Header().Get("Cache-Control") != "no-cache, no-store, must-revalidate" {
				t.Fatal("error status or retry policy changed")
			}
			if !strings.Contains(w.Body.String(), "https://codesamplex.dev/npm/x?lang=ko") || !strings.Contains(w.Body.String(), "<html lang=\"ko\">") {
				t.Fatal("current canonical or locale missing")
			}
		}
	}
}
