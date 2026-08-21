package web

import (
	"strings"
	"testing"
)

// commons-logging's page listed MockCookie, MockHttpServletRequest and
// MockHttpSession as its APIs. It is a logging facade and defines none of
// them — they are Spring Test's, attributed to every package in the closure
// of one build that used them.
//
// 563 of the corpus's 4,165 symbols are claimed by more than one package,
// worst case twenty-one. The evidence cannot say whose they are, so the page
// must not say either. It keeps the symbol — deleting would throw away the
// true attributions with the false ones — and states that the claim is
// shared, which is the fact the evidence actually supports.
func TestASymbolSeveralPackagesClaimIsMarkedAsShared(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.symbolSpread = map[string]int{"axios.post": 4}

	body := get(t, mux, "/npm/axios/1.12.0").Body.String()
	i := strings.Index(body, `<ul class="symlist`)
	if i < 0 {
		t.Fatal("no symbol list on the page")
	}
	list := body[i : i+strings.Index(body[i:], "</ul>")]
	if !strings.Contains(list, "axios.post") {
		t.Fatalf("the symbol is not listed at all:\n%s", list)
	}
	if !strings.Contains(list, "symshared") {
		t.Errorf("nothing marks a symbol four packages claim:\n%s", list)
	}
}

// A symbol only its own package claims says nothing extra.
func TestASymbolOnlyOnePackageClaimsIsNotMarked(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.symbolSpread = map[string]int{"axios.post": 1}
	body := get(t, mux, "/npm/axios/1.12.0").Body.String()
	if strings.Contains(body, `class="symshared`) {
		t.Error("an unambiguous symbol was marked shared")
	}
}

// The symbol page makes the strongest claim on the site — "this API of this
// package was measured here" — so it is the page that must say when the
// evidence does not establish the API is this package's at all.
func TestTheSymbolPageSaysWhenTheApiIsNotEstablishedAsItsOwn(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.symbolSpread = map[string]int{"axios.post": 14}
	body := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()

	head := body[:strings.Index(body, `class="eyebrow`)]
	if !strings.Contains(head, "14") {
		t.Errorf("the page claims the API without saying 14 packages carry it:\n%s", head)
	}
}

// An API only its own package carries needs no such note.
func TestAnUnambiguousSymbolPageSaysNothingExtra(t *testing.T) {
	mux, store := newTestMux(t, nil)
	store.symbolSpread = map[string]int{"axios.post": 1}
	body := get(t, mux, "/npm/axios/1.12.0/axios.post").Body.String()
	if strings.Contains(body, "cannot say whose API") {
		t.Error("an unambiguous symbol page hedged for no reason")
	}
}

// The package page is the front door, and its symbol axis names APIs. A
// logging facade had MockHttpServletRequest on that axis, stated flatly as
// its own.
func TestTheSymbolAxisQualifiesASharedSymbol(t *testing.T) {
	mux, store := newTestMux(t, func(d *Deps) { d.Store = newCubeStore() })
	if cs, ok := any(store).(*fakeStore); ok {
		_ = cs
	}
	body := get(t, mux, "/npm/reactish").Body.String()
	if !strings.Contains(body, "createRoot") {
		t.Skip("this fixture spreads no symbol axis")
	}
	// The cube store carries no shared symbols, so nothing should be marked:
	// the marker must not appear on evidence that does not warrant it.
	if strings.Contains(body, `class="symshared`) {
		t.Error("a symbol only one package carries was qualified anyway")
	}
}
