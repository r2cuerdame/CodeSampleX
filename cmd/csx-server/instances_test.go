package main

import "testing"

// The instance list is typed once into the environment, so a typo must not
// silently become a machine that costs nothing.
func TestParseInstances(t *testing.T) {
	got := parseInstances("csx-prod-1=12, csx-farm-linux-1=24.5 ,csx-farm-windows-1=44")
	if len(got) != 3 {
		t.Fatalf("got %d instances, want 3: %+v", len(got), got)
	}
	if got[0].Name != "csx-prod-1" || got[0].MonthlyUSD != 12 {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].MonthlyUSD != 24.5 {
		t.Errorf("fractional price lost: %+v", got[1])
	}
	for _, bad := range []string{"", "   ", "noequals", "name=", "=12", "name=abc", "name=-5"} {
		if out := parseInstances(bad); len(out) != 0 {
			t.Errorf("parseInstances(%q) = %+v, want nothing", bad, out)
		}
	}
	// One bad entry must not take the good ones with it, nor be counted.
	mixed := parseInstances("good=10,broken,other=5")
	if len(mixed) != 2 {
		t.Errorf("mixed = %+v, want the two well-formed entries", mixed)
	}
}
