package serverstore

import "testing"

// Migration 0052 adds another btree with sample_id as its leading key.
// PostgreSQL may choose either for sample-bounded reads. Keep all other index
// requirements exact; the integration tests still enforce measured IO bounds.
func hasBoundedIndex(indexes []string, required string) bool {
	for _, index := range indexes {
		if index == required || (required == "receipts_sample_idx" && index == "receipts_sample_peer_idx") {
			return true
		}
	}
	return false
}

func TestHasBoundedIndex(t *testing.T) {
	for _, tc := range []struct {
		name     string
		indexes  []string
		required string
		want     bool
	}{
		{"original sample key", []string{"receipts_sample_idx"}, "receipts_sample_idx", true},
		{"composite leading sample key", []string{"receipts_sample_peer_idx"}, "receipts_sample_idx", true},
		{"unrelated receipt index", []string{"receipts_created_result_idx"}, "receipts_sample_idx", false},
		{"similar name is insufficient", []string{"receipts_sample_idx_unbounded"}, "receipts_sample_idx", false},
		{"missing index", nil, "receipts_sample_idx", false},
		{"other requirement stays exact", []string{"receipts_sample_peer_idx"}, "receipts_created_result_idx", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasBoundedIndex(tc.indexes, tc.required); got != tc.want {
				t.Fatalf("hasBoundedIndex(%v, %q) = %v; want %v", tc.indexes, tc.required, got, tc.want)
			}
		})
	}
}
