package measurement

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCacheMetricUnknownRoundTripAndPresentation(t *testing.T) {
	r := RetrievalQuality{CacheBytes: 9, CacheBytesUnavailable: true}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if v, ok := fields["cacheBytes"]; !ok || v != nil {
		t.Fatalf("unknown cache serialized as value: %v", v)
	}
	var decoded RetrievalQuality
	if err = json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.CacheBytesUnavailable || !strings.Contains(decoded.CacheHumanMB(), "unavailable") {
		t.Fatal("unknown cache lost in decode/presentation")
	}
	text := NewTwoLayerReport("community", decoded, OutcomeValue{}).LayersText()
	if !strings.Contains(text, "Local cache:                 — (unavailable)") {
		t.Fatalf("unknown cache displayed as zero: %s", text)
	}
}
func TestCacheMetricKnownZeroAndPositiveStayNumeric(t *testing.T) {
	for _, n := range []int64{0, 1048576} {
		r := RetrievalQuality{CacheBytes: n}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err = json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["cacheBytes"] != float64(n) {
			t.Fatalf("known bytes changed: %v", fields["cacheBytes"])
		}
		var decoded RetrievalQuality
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.CacheBytesUnavailable || decoded.CacheBytes != n {
			t.Fatal("known value became unknown")
		}
	}
}
