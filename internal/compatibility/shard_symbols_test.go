package compatibility

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// The shard schema is additive: upgraded readers see manifest symbols and
// exact depth totals, while readers compiled against the previous v1 shape
// continue to decode the package and sample they already understood.
func TestShardSymbolCoverageRoundTripsAndOldReadersIgnoreIt(t *testing.T) {
	want := ShardPackage{
		PURL:                      "pkg:golang/github.com/acme/widget@1.2.3",
		CanonicalCaseCountTotal:   41,
		DistinctSubjectCountTotal: 73,
		Samples: []ShardSample{{
			SampleID:         "sha256:sample",
			Status:           "PUBLISHED",
			License:          "MIT-0",
			Symbols:          []string{"Client.Close", "github.com/acme/widget.Client.Do"},
			SymbolsTruncated: true,
		}},
	}
	raw, _ := BuildShard("golang/github.com/acme/widget/1", []ShardPackage{want}, testNow)

	var current Shard
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		t.Fatal(err)
	}
	if len(current.Packages) != 1 {
		t.Fatalf("packages = %d, want 1", len(current.Packages))
	}
	got := current.Packages[0]
	if got.CanonicalCaseCountTotal != want.CanonicalCaseCountTotal ||
		got.DistinctSubjectCountTotal != want.DistinctSubjectCountTotal {
		t.Fatalf("depth totals = (%d, %d), want (%d, %d)",
			got.CanonicalCaseCountTotal, got.DistinctSubjectCountTotal,
			want.CanonicalCaseCountTotal, want.DistinctSubjectCountTotal)
	}
	if len(got.Samples) != 1 || !reflect.DeepEqual(got.Samples[0].Symbols, want.Samples[0].Symbols) ||
		!got.Samples[0].SymbolsTruncated {
		t.Fatalf("sample symbol coverage did not round-trip: %+v", got.Samples)
	}

	// This mirrors the fields known to an older client. encoding/json ignores
	// the additive fields and the v1 document remains usable.
	var old struct {
		SchemaVersion int `json:"schemaVersion"`
		Packages      []struct {
			PURL    string `json:"purl"`
			Samples []struct {
				SampleID string `json:"sampleId"`
			} `json:"samples"`
		} `json:"packages"`
	}
	if err := json.Unmarshal([]byte(raw), &old); err != nil {
		t.Fatalf("old reader rejected additive v1 fields: %v", err)
	}
	if old.SchemaVersion != 1 || len(old.Packages) != 1 ||
		old.Packages[0].Samples[0].SampleID != "sha256:sample" {
		t.Fatalf("old reader lost its known fields: %+v", old)
	}
}

func TestShardSymbolsApplyPublicRulesWithoutRejectingAPIPathSyntax(t *testing.T) {
	invalidUTF8 := string([]byte{'b', 'a', 'd', 0xff})
	bounded, all, truncated := symbolsForShard([]string{
		" github.com/acme/widget.Client.Do ",
		"User#active?",
		"Type[T]",
		"",
		"\u2003", // Unicode whitespace only.
		"line\nbreak",
		strings.Repeat("x", maxPublicSymbolBytes+1),
		invalidUTF8,
	})
	want := []string{"Type[T]", "User#active?", "github.com/acme/widget.Client.Do"}
	if !reflect.DeepEqual(all, want) || !reflect.DeepEqual(bounded, want) {
		t.Fatalf("safe symbols = %q / %q, want %q", bounded, all, want)
	}
	if !truncated {
		t.Fatal("invalid declarations were dropped without symbolsTruncated")
	}
}

func TestShardSymbolsDedupeCaseSensitively(t *testing.T) {
	bounded, all, truncated := symbolsForShard([]string{
		" Widget.Run ", "Widget.Run", "widget.run", "Widget.Run",
	})
	want := []string{"Widget.Run", "widget.run"}
	if !reflect.DeepEqual(bounded, want) || !reflect.DeepEqual(all, want) {
		t.Fatalf("deduped symbols = %q / %q, want %q", bounded, all, want)
	}
	if truncated {
		t.Fatal("exact duplicates do not make the distinct symbol set incomplete")
	}
}

func TestShardSymbolsBoundOverflowAndDiscloseIt(t *testing.T) {
	raw := make([]string, 0, maxShardSampleSymbols+5)
	for i := maxShardSampleSymbols + 4; i >= 0; i-- {
		raw = append(raw, fmt.Sprintf("pkg.Symbol%02d", i))
	}
	bounded, all, truncated := symbolsForShard(raw)
	if len(bounded) != maxShardSampleSymbols || len(all) != maxShardSampleSymbols+5 {
		t.Fatalf("bounded/all lengths = %d/%d, want %d/%d",
			len(bounded), len(all), maxShardSampleSymbols, maxShardSampleSymbols+5)
	}
	if bounded[0] != "pkg.Symbol00" || bounded[len(bounded)-1] != "pkg.Symbol31" {
		t.Fatalf("overflow was not capped after deterministic sort: %q", bounded)
	}
	if !truncated {
		t.Fatal("overflow was capped without symbolsTruncated")
	}
}

// Counts are taken from every matching sample and every safe declared symbol
// before either the 20-sample cap or the 32-symbol wire cap is applied.
func TestShardDepthTotalsAreExactBeforeBothCaps(t *testing.T) {
	purl := "pkg:npm/axios@1.12.0"
	p, err := domain.ParsePURL(purl)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var samples []sampleData
	for i := 0; i < maxShardSamples+5; i++ {
		canonical := i
		if i == 1 {
			canonical = 0 // two samples, one canonical case
		}
		symbol := fmt.Sprintf("axios.Subject%02d", canonical)
		symbols := []string{symbol}
		if i == maxShardSamples+4 {
			for n := 0; n < 40; n++ {
				symbols = append(symbols, fmt.Sprintf("axios.Extra%02d", n))
			}
		}
		cse := domain.Case{
			SchemaVersion: 1,
			Kind:          "HOW",
			Goal:          fmt.Sprintf("canonical case %02d", canonical),
			Packages:      []string{purl},
			Contract:      []string{"assert the behavior"},
		}
		samples = append(samples, sampleData{
			row: serverstore.SampleRow{
				SampleID:  fmt.Sprintf("sha256:sample%02d", i),
				Status:    "PUBLISHED",
				License:   "MIT-0",
				CreatedAt: base.Add(time.Duration(i) * time.Hour),
			},
			manifest: domain.SampleManifest{
				SchemaVersion: 1,
				Case:          cse,
				Packages:      []string{purl},
				Symbols:       symbols,
			},
			purls: []domain.PURL{p},
		})
	}

	got := shardSamplesFor(samples, "npm", "axios", "1")
	if len(got.Samples) != maxShardSamples {
		t.Fatalf("visible samples = %d, want capped %d", len(got.Samples), maxShardSamples)
	}
	if got.CanonicalCaseCountTotal != maxShardSamples+4 {
		t.Fatalf("canonical case total = %d, want %d",
			got.CanonicalCaseCountTotal, maxShardSamples+4)
	}
	// 24 primary subjects (the first two are the same) plus 40 extra
	// symbols on one sample, including the eight that do not fit its wire list.
	if got.DistinctSubjectCountTotal != 64 {
		t.Fatalf("distinct subject total = %d, want 64", got.DistinctSubjectCountTotal)
	}
}

func TestShardSymbolOrderIsDeterministicAcrossManifestOrder(t *testing.T) {
	render := func(raw []string) string {
		bounded, _, truncated := symbolsForShard(raw)
		body, _ := BuildShard("npm/widget/1", []ShardPackage{{
			PURL: "pkg:npm/widget@1.0.0",
			Samples: []ShardSample{{
				SampleID:         "sha256:sample",
				Symbols:          bounded,
				SymbolsTruncated: truncated,
			}},
		}}, testNow)
		return body
	}
	a := render([]string{"widget.z", " widget.a ", "widget.m", "widget.a"})
	b := render([]string{"widget.a", "widget.m", "widget.z", " widget.a "})
	if a != b {
		t.Fatalf("manifest order changed canonical shard JSON:\n%s\n%s", a, b)
	}
}

func TestPublicShardSchemaDeclaresBoundedSymbolCoverageAndExactTotals(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "schemas", "v1", "shard.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	packages := properties["packages"].(map[string]any)
	packageItems := packages["items"].(map[string]any)
	packageProperties := packageItems["properties"].(map[string]any)
	for _, name := range []string{"canonicalCaseCountTotal", "distinctSubjectCountTotal"} {
		if _, ok := packageProperties[name]; !ok {
			t.Errorf("public shard schema is missing %s", name)
		}
	}
	samples := packageProperties["samples"].(map[string]any)
	sampleItems := samples["items"].(map[string]any)
	sampleProperties := sampleItems["properties"].(map[string]any)
	symbols := sampleProperties["symbols"].(map[string]any)
	if got := int(symbols["maxItems"].(float64)); got != maxShardSampleSymbols {
		t.Errorf("schema symbols maxItems = %d, want %d", got, maxShardSampleSymbols)
	}
	if _, ok := sampleProperties["symbolsTruncated"]; !ok {
		t.Error("public shard schema is missing symbolsTruncated")
	}
}
