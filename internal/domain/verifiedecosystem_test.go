package domain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Every ecosystem this network verifies samples in has to be one it can
// record a run in. A contract run is an execution on a real machine, and
// refusing to record it leaves the coordinate reading "never measured" about
// work we did ourselves.
//
// Measured after the receipt backfill: 8,467 runs were recorded and 1,467
// refused, and every refusal was gem, hex or pub. The 938 snapshot rows left
// reading "never measured" were exactly those three ecosystems and nothing
// else.
//
// They sit where maven already sits. The client scanner ships adapters for
// npm, pypi, golang and cargo only; maven is on this list because a
// verification-only ecosystem may publish signed sample evidence without
// scanning anybody's local project, which is the same standing gem, hex and
// pub have.
//
// composer was the fourth and was missed the same way (#319), so the list is
// read from the published adapter matrix rather than typed here: an adapter
// that claims A4 verifies samples, and its ecosystem must record the run.
func TestEveryVerifiedEcosystemCanRecordARun(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(schemaDir(t), "adapters.json"))
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Adapters []struct {
			Ecosystem    string   `json:"ecosystem"`
			Capabilities []string `json:"capabilities"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal(raw, &matrix); err != nil {
		t.Fatal(err)
	}
	verified := 0
	for _, adapter := range matrix.Adapters {
		if !slices.Contains(adapter.Capabilities, "A4") {
			continue
		}
		verified++
		if !AllowedEcosystems[adapter.Ecosystem] {
			t.Errorf("%s samples are verified but a run in one cannot be recorded", adapter.Ecosystem)
		}
	}
	if verified < 9 {
		t.Fatalf("adapters.json lists %d A4 adapters, want at least 9", verified)
	}
}
