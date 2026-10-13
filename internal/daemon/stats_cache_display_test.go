package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStatsKnownEmptyCASRemainsMeasuredZero(t *testing.T) {
	d, _ := cacheContractFixture(t)
	defer d.Close()
	st, err := d.StatsNow(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["cacheBytes"] != float64(0) || st.CacheBytesUnavailable {
		t.Fatalf("known empty cache not numeric zero: %s", raw)
	}
	if statsCacheHuman(st) == "— (unavailable)" {
		t.Fatal("known zero displayed as unknown")
	}
}
func TestStatsNullCacheSurvivesClientDecodeAndUI(t *testing.T) {
	var st Stats
	if err := json.Unmarshal([]byte("{\"cacheBytes\":null,\"evidenceBatchesSent\":37,\"queueDepth\":1,\"queue\":{\"evidenceBatches\":0,\"uploads\":1}}"), &st); err != nil {
		t.Fatal(err)
	}
	if st.EvidenceBatchesSent != 37 || st.QueueDepth != 1 || !st.CacheBytesUnavailable {
		t.Fatal("optional cache changed core facts")
	}
	if statsCacheHuman(st) != "— (unavailable)" || !strings.Contains(st.CacheHumanMB(), "unavailable") {
		t.Fatal("UI/CLI converted unknown cache to zero")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\"cacheBytes\":null") {
		t.Fatal("daemon client serialized unknown as zero")
	}
}
func TestStatsMissingCacheIsUnrecordedAndTopMetricDrivesCLI(t *testing.T) {
	var st Stats
	if err := json.Unmarshal([]byte("{\"queueDepth\":1}"), &st); err != nil {
		t.Fatal(err)
	}
	if !st.CacheBytesUnavailable {
		t.Fatal("missing cache became measured zero")
	}
	st = Stats{CacheBytes: 1048576}
	if st.CacheHumanMB() != "1.0 MB" {
		t.Fatalf("CLI borrowed a different nested value: %s", st.CacheHumanMB())
	}
}
