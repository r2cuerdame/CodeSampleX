package serverstore

import (
	"testing"
	"time"
)

func TestDecodeAdminDailyStatPreservesMissingAndMeasuredZero(t *testing.T) {
	day := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	row := decodeAdminDailyStat(day, `{"evidence":0,"verifiedSamples":12}`)
	if !row.Evidence.Valid || row.Evidence.Value != 0 {
		t.Fatalf("measured zero evidence = %+v", row.Evidence)
	}
	if !row.VerifiedSamples.Valid || row.VerifiedSamples.Value != 12 {
		t.Fatalf("verified samples = %+v", row.VerifiedSamples)
	}
	if row.Packages.Valid {
		t.Fatalf("missing packages became measured zero: %+v", row.Packages)
	}

	malformed := decodeAdminDailyStat(day, `{not-json`)
	if malformed.Evidence.Valid || malformed.VerifiedSamples.Valid || malformed.Packages.Valid {
		t.Fatalf("malformed document produced metrics: %+v", malformed)
	}
	negative := decodeAdminDailyStat(day, `{"evidence":-1,"verifiedSamples":0,"packages":0}`)
	if negative.Evidence.Valid || !negative.VerifiedSamples.Valid || !negative.Packages.Valid {
		t.Fatalf("negative/missing validation = %+v", negative)
	}
}

func TestDecodeAdminPackageKeySupportsScopedAndNestedNames(t *testing.T) {
	tests := []struct {
		key       string
		ecosystem string
		name      string
		ok        bool
	}{
		{"pkg:npm/%40types/node", "npm", "@types/node", true},
		{"pkg:golang/golang.org/x/crypto", "golang", "golang.org/x/crypto", true},
		{"npm/axios", "", "", false},
		{"pkg:npm/", "", "", false},
	}
	for _, tc := range tests {
		ecosystem, name, ok := decodeAdminPackageKey(tc.key)
		if ecosystem != tc.ecosystem || name != tc.name || ok != tc.ok {
			t.Errorf("decode(%q) = (%q,%q,%v), want (%q,%q,%v)", tc.key, ecosystem, name, ok, tc.ecosystem, tc.name, tc.ok)
		}
	}
}
