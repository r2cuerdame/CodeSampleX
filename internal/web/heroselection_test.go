package web

import (
	"net/http/httptest"
	"testing"
)

// Clicking a category must not empty the grid.
//
// Reported from the live homepage: browsing the "where it actually ran"
// categories occasionally leaves no matrix at all. It is not occasional and it
// is not random — it happens whenever the clicked package's cube is not
// already cached.
//
// The selected view narrows `ordered` to that one package, so the first cold
// cube aborts the whole build; and the memo is keyed on the selection, so a
// package just clicked for the first time has nothing to fall back to. The
// reader is then shown "the first compatibility grids appear here as soon as
// the network records enough environment evidence" — on a network with tens of
// thousands of observations, which is the same false empty state this file
// already fixed once for the unselected view.
//
// The unselected grid is the honest thing to show meanwhile. It is a grid this
// process really rendered, it contains the clicked package among its rows, and
// it carries its own observation date. One click behind is not the same as
// "the network is empty".
func TestClickingAColdCategoryKeepsTheGridThatWasAlreadyThere(t *testing.T) {
	hits, store := heroHits(3)
	s := &site{d: Deps{Store: store}}

	// The reader lands on the unselected page first and sees a grid.
	unselected := waitHeroMatrix(t, s, httptest.NewRequest("GET", "/", nil), "en", hits)
	if unselected == nil {
		t.Fatal("the unselected landing never rendered a grid")
	}

	// Then they click a category whose cube this process has not assembled.
	cold := httptest.NewRequest("GET", "/?m=npm/pkg2", nil)
	if got := s.heroMatrix(cold, "en", hits); got == nil {
		t.Error("clicking a category emptied the grid; the reader is told the network has no evidence")
	}
}
