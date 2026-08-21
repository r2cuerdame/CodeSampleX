package evidence

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The network can see the demand it cannot satisfy and is blind to the demand
// it does.
//
// A MISS uploads a Wanted ask, so misses arrive: 648 of them in the first
// week. A HIT writes a local hits row that is never uploaded, so the server
// has recorded five hits in its lifetime — while 130 distinct samples were
// adopted, and a sample can only be adopted after a hit surfaced it.
//
// Every metric the product is steered by needs the hit as its denominator:
// samples shown per search, applied per shown. Without it the numerator
// arrives alone and there is nothing to divide by.
func TestAHitIsQueuedForUploadTheWayAMissIs(t *testing.T) {
	db, ident := testDB(t), testIdentity(t)
	cfg := &config.Config{Mode: config.ModeCommunity}
	ctx := context.Background()

	QueueSearchHit(ctx, db, ident, cfg, SearchHit{
		Grade:        domain.GradeExact,
		ResultsShown: 3,
		OfferID:      "offer-1",
	})

	items, err := db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queued %d items, want the hit", len(items))
	}
	if items[0].Kind != SearchHitQueueKind {
		t.Errorf("kind = %q, want the search-hit kind", items[0].Kind)
	}
}

// Counts only. The question, the packages and the environment are the
// caller's business and stay on the caller's machine — the same rule the
// local hits table has always followed, and the reason hits were never
// uploaded in the first place.
func TestAQueuedHitCarriesNoQuestion(t *testing.T) {
	db, ident := testDB(t), testIdentity(t)
	cfg := &config.Config{Mode: config.ModeCommunity}
	ctx := context.Background()

	QueueSearchHit(ctx, db, ident, cfg, SearchHit{
		Grade:        domain.GradeExact,
		ResultsShown: 2,
		OfferID:      "offer-2",
		SampleID:     "sha256:aaa",
	})
	items, _ := db.QueuePending(ctx, 10)
	if len(items) == 0 {
		t.Fatal("nothing queued")
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(items[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"query", "packages", "symbols", "environment", "projectPackages"} {
		if _, present := payload[forbidden]; present {
			t.Errorf("the payload carries %q, which never leaves the machine", forbidden)
		}
	}
	if payload["resultsShown"] == nil || payload["grade"] == nil {
		t.Errorf("payload = %v, want the counts the denominator needs", payload)
	}
}

// Local-only mode uploads nothing at all. It is the mode that exists to stop
// exactly this, and a counts-only payload is not an exception to it.
func TestLocalOnlyModeQueuesNoHit(t *testing.T) {
	db, ident := testDB(t), testIdentity(t)
	ctx := context.Background()

	QueueSearchHit(ctx, db, ident, &config.Config{Mode: "local-only"}, SearchHit{
		Grade: domain.GradeExact, ResultsShown: 1, OfferID: "offer-3",
	})
	items, _ := db.QueuePending(ctx, 10)
	if len(items) != 0 {
		t.Errorf("local-only mode queued %d items, want none", len(items))
	}
}
