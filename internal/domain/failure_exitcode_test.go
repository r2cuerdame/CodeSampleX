package domain

import (
	"strconv"
	"testing"
)

func TestCanonicalExitCodeSignedAndLegacyWindowsBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   int
		want int
		ok   bool
	}{
		{"signed minimum", -1 << 31, -1 << 31, true},
		{"ordinary", 1, 1, true},
		{"signed maximum", 1<<31 - 1, 1<<31 - 1, true},
	} {
		got, ok := CanonicalExitCode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: CanonicalExitCode(%d) = (%d,%v), want (%d,%v)", tc.name, tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if strconv.IntSize < 64 {
		return
	}
	legacyMin := int(uint64(1 << 31))
	legacyMax := int(uint64(1<<32 - 1))
	tooLarge := int(uint64(1 << 32))
	for _, tc := range []struct {
		name string
		in   int
		want int
		ok   bool
	}{
		{"legacy upper-half minimum", legacyMin, -1 << 31, true},
		{"legacy DWORD minus one", legacyMax, -1, true},
		{"above DWORD", tooLarge, 0, false},
		{"below signed minimum", (-1 << 31) - 1, 0, false},
	} {
		got, ok := CanonicalExitCode(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: CanonicalExitCode(%d) = (%d,%v), want (%d,%v)", tc.name, tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
