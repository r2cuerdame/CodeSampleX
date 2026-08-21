package httpapi

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// searchHitReport is the counts-only record one successful search sends.
//
// No query, no packages, no symbols, no environment. Those stay on the
// caller's machine — that is why hits were never uploaded at all — and this
// route exists to carry the count, not the question.
type searchHitReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Epoch         string `json:"epoch"`
	AnonID        string `json:"anonId"`
	Grade         string `json:"grade"`
	ResultsShown  int    `json:"resultsShown"`
	SampleID      string `json:"sampleId,omitempty"`
	OfferID       string `json:"offerId,omitempty"`
}

// handleSearchHit implements POST /v1/search-hits.
//
// The network could see the demand it could not satisfy and nothing of the
// demand it could. A miss uploads a Wanted ask, so misses arrive — 648 of
// them in the first week. A hit wrote a row in the caller's local hits table
// and stopped there, so the server had recorded five hits in its lifetime
// while 130 distinct samples were adopted, and a sample can only be adopted
// after a hit surfaced it.
//
// The counter that looked like search volume, search_outcomes_daily, only
// moves when something calls the HTTP search endpoint directly — and the only
// HTTP search client in the repo had no callers at all. So "samples shown per
// search" and "applied per shown" could not be computed here, and "nobody is
// searching" was read off a number measuring something else.
//
// Anonymous like every other write. What keeps it honest is the primary key:
// one reporter counts once per offer per day, so an agent retrying a search
// all afternoon is one hit.
func (a *api) handleSearchHit(w http.ResponseWriter, r *http.Request) {
	var req searchHitReport
	if !readJSON(w, r, 1<<16, &req) {
		return
	}
	if req.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "search hit schemaVersion must be 1")
		return
	}
	if req.Epoch == "" || req.AnonID == "" {
		writeErr(w, http.StatusBadRequest, "search hit requires epoch and anonId")
		return
	}
	// A hit with nothing shown is not a hit. Accepting it would let a client
	// inflate the denominator without ever surfacing an answer.
	if req.ResultsShown <= 0 {
		writeErr(w, http.StatusBadRequest, "search hit requires resultsShown above zero")
		return
	}
	if err := a.d.Store.RecordSearchHit(r.Context(), serverstore.SearchHitRow{
		Grade:        req.Grade,
		ResultsShown: req.ResultsShown,
		SampleID:     req.SampleID,
		OfferID:      req.OfferID,
		Epoch:        req.Epoch,
		AnonID:       req.AnonID,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "search hit not recorded")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
