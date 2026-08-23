package serverstore

import "testing"

// The miss side of the No-match rate has to count the same unit the hit side
// counts: one question a reporter asked, once. A report names up to ten
// package coordinates, and counting its rows would make a single miss that
// mentioned three packages outrank three separate misses.
func TestSearchMissKeyIsOneQuestionRegardlessOfHowManyCoordinatesItNames(t *testing.T) {
	one := searchMissKey([]WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}})
	three := searchMissKey([]WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0"},
		{Ecosystem: "npm", Name: "zod", Version: "3.23.8"},
		{Ecosystem: "npm", Name: "vite", Version: "5.4.0"},
	})
	if one == "" || three == "" {
		t.Fatal("a report with coordinates produced no key")
	}
	if one == three {
		t.Fatal("two different questions collapsed into one key")
	}
}

// The daemon batches reports and the server expands each into rows; neither
// promises an order. A key that depends on it would let the same question
// count twice on a retry, which is the one thing this counter must not do.
func TestSearchMissKeyIgnoresCoordinateOrder(t *testing.T) {
	forward := searchMissKey([]WantedRow{
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"},
		{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"},
	})
	reversed := searchMissKey([]WantedRow{
		{Ecosystem: "pypi", Name: "requests", Version: "2.32.3"},
		{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"},
	})
	if forward != reversed {
		t.Fatalf("order changed the key: %q vs %q", forward, reversed)
	}
}

// A miss about a symbol is a different question from a miss about the package,
// and a miss on Windows is a different question from the same miss on Linux —
// that distinction is why wanted rows carry both.
func TestSearchMissKeySeparatesSymbolAndPlatform(t *testing.T) {
	base := WantedRow{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	withSymbol := base
	withSymbol.Symbol = "axios.post"
	withOS := base
	withOS.TargetOS = "windows"

	keys := map[string]string{
		"package": searchMissKey([]WantedRow{base}),
		"symbol":  searchMissKey([]WantedRow{withSymbol}),
		"os":      searchMissKey([]WantedRow{withOS}),
	}
	for a, keyA := range keys {
		for b, keyB := range keys {
			if a < b && keyA == keyB {
				t.Errorf("%s and %s share a key", a, b)
			}
		}
	}
}

// An empty report is not a question. Counting it would let a caller move the
// denominator without ever having searched.
func TestSearchMissKeyRefusesAnEmptyReport(t *testing.T) {
	if key := searchMissKey(nil); key != "" {
		t.Fatalf("an empty report produced key %q", key)
	}
}
