package serverstore

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

// #517: the builder's pass record round-trips identically through the Fake
// and PostgreSQL, including the overwrite a second pass makes.
func TestIntegrationBuilderStatusParity(t *testing.T) {
	ctx := context.Background()
	stores := map[string]BuilderStatusStore{"fake": NewFake(), "pg": openTestPG(t)}
	first := `{"lastPassOutcome":"timeout","lastFailureReason":"timeout","consecutiveFailures":1,"repair":{"cursor":"npm/axios","packagesDone":2}}`
	second := `{"lastPassOutcome":"success","lastSuccessAt":"2026-09-26T02:00:00Z","consecutiveFailures":0}`
	for name, store := range stores {
		if _, ok, err := store.GetBuilderStatus(ctx, "compatibility-builder"); err != nil || ok {
			t.Fatalf("%s: empty store answered ok=%t err=%v", name, ok, err)
		}
		for _, doc := range []string{first, second} {
			if err := store.PutBuilderStatus(ctx, "compatibility-builder", doc); err != nil {
				t.Fatalf("%s: put: %v", name, err)
			}
			got, ok, err := store.GetBuilderStatus(ctx, "compatibility-builder")
			if err != nil || !ok {
				t.Fatalf("%s: get ok=%t err=%v", name, ok, err)
			}
			var gotV, wantV any
			if json.Unmarshal([]byte(got), &gotV) != nil || json.Unmarshal([]byte(doc), &wantV) != nil ||
				!reflect.DeepEqual(gotV, wantV) {
				t.Fatalf("%s: round trip = %s, want %s", name, got, doc)
			}
		}
		if _, ok, _ := store.GetBuilderStatus(ctx, "another-builder"); ok {
			t.Fatalf("%s: status leaked across builder names", name)
		}
	}
}
