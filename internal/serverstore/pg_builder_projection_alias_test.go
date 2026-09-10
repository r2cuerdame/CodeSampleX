package serverstore

import (
	"strings"
	"testing"
)

func TestBuilderProjectionRejectsCaseFoldAliases(t *testing.T) {
	for _, raw := range []string{
		`{"Packages":["pkg:npm/other@1.0.0"]}`,
		`{"packages":["pkg:npm/selected@1.0.0"],"Packages":["pkg:npm/other@1.0.0"]}`,
		`{"Symbols":["other.call"]}`,
		`{"Subject":"pkg:npm/other@1.0.0"}`,
		`{"pacKages":["pkg:npm/other@1.0.0"]}`,
		`{"ſubject":"pkg:npm/other@1.0.0"}`,
	} {
		if _, err := deriveSampleBuilderProjection(raw); err == nil || !strings.Contains(err.Error(), "field alias") {
			t.Errorf("ambiguous sample accepted: %s err=%v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"SchemaVersion":2,"stages":{"resolve":"PASS"}}`,
		`{"schemaVersion":2,"Stages":{"resolve":"PASS"}}`,
		`{"schemaVersion":2,"stages":{"resolve":"PASS"},"ResolvedPackages":["pkg:npm/other@1.0.0"]}`,
		`{"schemaVersion":2,"SchemaVersion":1,"stages":{"resolve":"PASS"}}`,
		`{"schemaVersion":2,"ſtages":{"resolve":"PASS"}}`,
	} {
		if _, err := deriveReceiptBuilderProjection(raw); err == nil || !strings.Contains(err.Error(), "field alias") {
			t.Errorf("ambiguous receipt accepted: %s err=%v", raw, err)
		}
	}
}

func TestBuilderReceiptProjectionMatchesExactSQLClaimHeader(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		claim    bool
		packages int
	}{
		{`{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0"]}`, true, 1},
		{`{"schemaVersion":2,"stages":{"Resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0"]}`, false, 0},
		{`{"schemaVersion":1,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0"]}`, false, 0},
		{`{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0","invalid"]}`, true, 0},
		{`{}`, false, 0},
	} {
		got, err := deriveReceiptBuilderProjection(tc.raw)
		if err != nil || got.claim != tc.claim || len(got.packages) != tc.packages {
			t.Errorf("header projection %s: got=%+v err=%v", tc.raw, got, err)
		}
	}
}
