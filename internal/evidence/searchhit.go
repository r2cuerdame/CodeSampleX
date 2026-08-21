package evidence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// SearchHitQueueKind is the upload queue kind for a search that found
// something. It is deliberately its own kind: a wanted ask is a package the
// network should learn, and a hit is a count.
const SearchHitQueueKind = "search-hit"

// searchHitSchemaVersion travels with the payload so the server can refuse a
// shape it does not know rather than guess at one.
const searchHitSchemaVersion = 1

// SearchHit is what one successful search is allowed to say out loud.
//
// It carries no question. Not the query, not the packages, not the symbols,
// not the environment — those are the caller's and stay on the caller's
// machine, which is the rule the local hits table has always followed and
// the reason hits were never uploaded at all.
type SearchHit struct {
	// Grade is the match grade of the top result, as a bucket.
	Grade domain.MatchGrade
	// ResultsShown is how many results the caller was handed.
	ResultsShown int
	// OfferID ties this hit to the adoption that may follow it. It is an
	// opaque local capability with nothing derivable in it, and it is the
	// only thing that can turn "applied" into "applied out of how many
	// shown" — the adoptions table has a numerator and no denominator.
	OfferID string
	// SampleID is a content address of an already-published sample: public
	// by construction, and what makes "which answers get used" answerable.
	SampleID string
}

// QueueSearchHit records that a search found something, for upload.
//
// The network can see the demand it cannot satisfy and is blind to the demand
// it does. A miss uploads a Wanted ask, so misses arrive — 648 of them in the
// first week. A hit wrote a local row that never left the machine, so the
// server had recorded five hits in its lifetime while 130 distinct samples
// were adopted, and a sample can only be adopted after a hit surfaced it.
//
// Every rate the product is steered by needs this as its denominator: samples
// shown per search, applied per shown. Without it the numerator arrives alone.
//
// Community mode only, like every upload, and counts only.
func QueueSearchHit(ctx context.Context, db *localdb.DB, ident *identity.Identity,
	cfg *config.Config, hit SearchHit) {

	if db == nil || ident == nil || cfg == nil || cfg.Mode != config.ModeCommunity {
		return
	}
	if hit.ResultsShown <= 0 {
		return
	}
	now := time.Now().UTC()
	epoch := now.Format("2006-01-02")
	payload := map[string]any{
		"schemaVersion": searchHitSchemaVersion,
		"epoch":         epoch,
		// The same rotating anonymous id every other upload uses: it makes
		// "searches per agent per day" countable without naming anyone.
		"anonId":       ident.AnonID(epoch),
		"grade":        string(hit.Grade),
		"resultsShown": hit.ResultsShown,
	}
	if hit.OfferID != "" {
		payload["offerId"] = hit.OfferID
	}
	if hit.SampleID != "" {
		payload["sampleId"] = hit.SampleID
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = db.Enqueue(ctx, SearchHitQueueKind, string(b))
}
