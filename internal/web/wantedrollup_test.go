package web

import "testing"

// The board is a demand ranking, but the row is a work unit: package, version,
// symbol and now platform. Rendered raw it repeats the same package four
// times with "1" beside each, and a reader concludes the page is broken
// rather than that four different symbols were asked about.
func TestWantedRollsUpToTheCoordinateAReaderRecognises(t *testing.T) {
	rows := []WantedRow{
		{Ecosystem: "golang", Name: "example.com/caddy", Version: "2.11.4", Symbol: "caddy.Run", Asks: 1, TargetOS: "windows"},
		{Ecosystem: "golang", Name: "example.com/caddy", Version: "2.11.4", Symbol: "caddy.Stop", Asks: 1, TargetOS: "windows"},
		{Ecosystem: "golang", Name: "example.com/caddy", Version: "2.11.4", Symbol: "caddy.Run", Asks: 2, TargetOS: "linux"},
		{Ecosystem: "pypi", Name: "django", Version: "6.1", Symbol: "", Asks: 1},
	}
	got, truncated := rollUpWanted(rows, 10)
	if truncated {
		t.Error("a four-row window reported truncation")
	}
	if len(got) != 2 {
		t.Fatalf("rolled up to %d rows, want one per package version: %+v", len(got), got)
	}
	caddy := got[0]
	if caddy.Name != "example.com/caddy" {
		t.Fatalf("most-asked first put %q on top", caddy.Name)
	}
	if caddy.Asks != 4 {
		t.Errorf("asks = %d, want the four reports summed", caddy.Asks)
	}
	if caddy.Symbols != 2 {
		t.Errorf("symbols = %d, want the two distinct APIs asked about", caddy.Symbols)
	}
	// The names survive the fold: with one API asked about, naming it beats
	// counting it, and the count only rescues the many-rows case.
	if len(caddy.SymbolNames) != 2 || caddy.SymbolNames[0] != "caddy.Run" {
		t.Errorf("symbol names = %v, want them kept and sorted", caddy.SymbolNames)
	}
	if len(caddy.Platforms) != 2 {
		t.Errorf("platforms = %v, want windows and linux kept apart", caddy.Platforms)
	}

	// A package asked about with no symbol and no platform is a whole-package
	// question, and must not claim either.
	django := got[1]
	if django.Symbols != 0 || len(django.Platforms) != 0 {
		t.Errorf("a bare package question invented detail: %+v", django)
	}
}

// Silent truncation reads as "this is everything". The window has to admit
// when it cut something off.
func TestWantedRollupAdmitsATruncatedWindow(t *testing.T) {
	rows := make([]WantedRow, 0, 5)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		rows = append(rows, WantedRow{Ecosystem: "npm", Name: n, Version: "1.0.0", Asks: 1})
	}
	if _, truncated := rollUpWanted(rows, 5); !truncated {
		t.Error("a full window did not report that it may have cut rows")
	}
}
