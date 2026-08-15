package domain

import "testing"

func TestZZScratchCompareVersionsWeakOrdering(t *testing.T) {
	vs := []string{
		"", "0", "1", "1.0", "1.0.0", "1.00", "1.0.0.0",
		"0.9.9", "0.10.0", "14.0.1", "7.0.3", "v1.2.0", "1.2.0",
		"2.0.0-alpha", "2.0.0-alpha.1", "2.0.0-rc1", "2.0.0-rc2", "2.0.0-rc10",
		"2.0.0", "2.0.0+meta", "2.0.0+build-9", "1.2.3.4", "next", "1.x",
		"3.11.0b1", "3.11.0rc1", "1.0.0-1", "1.0.0-0.3.7", "1.0.0-x.7.z.92",
		"v0.0.0-20210101120000-abcdef123456", "v1.2.3+incompatible",
		"22.18.1", "22.9.0", "1.0.0-alpha", "1.0.0-alpha.beta", "1.0.0-beta",
	}
	for _, a := range vs {
		for _, b := range vs {
			if CompareVersions(a, b) != -CompareVersions(b, a) {
				t.Errorf("antisymmetry: %q vs %q", a, b)
			}
		}
	}
	for _, a := range vs {
		for _, b := range vs {
			for _, c := range vs {
				ab, bc, ac := CompareVersions(a, b), CompareVersions(b, c), CompareVersions(a, c)
				if ab < 0 && bc < 0 && ac >= 0 {
					t.Errorf("transitivity(<): %q<%q<%q but cmp(a,c)=%d", a, b, c, ac)
				}
				if ab == 0 && bc == 0 && ac != 0 {
					t.Errorf("transitivity(=): %q==%q==%q but cmp(a,c)=%d", a, b, c, ac)
				}
			}
		}
	}
	t.Logf("build metadata with a hyphen: cmp(%q,%q)=%d", "2.0.0+build-9", "2.0.0", CompareVersions("2.0.0+build-9", "2.0.0"))
	t.Logf("cmp(%q,%q)=%d", "3.11.0b1", "3.11.0", CompareVersions("3.11.0b1", "3.11.0"))
}
