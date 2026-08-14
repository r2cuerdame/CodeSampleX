package httpapi

import (
	"net/http"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// maxWantedPerReport bounds one report. A caller with a large dependency
// tree could otherwise file a hundred rows from a single miss and drown the
// ranking; the packages that mattered to the question are the first few.
const maxWantedPerReport = 10

// wantedReport is what a peer sends after a NO_SAFE_MATCH.
//
// The QUESTION IS NOT IN IT, and that is the point. A typed question
// carries project names, file paths, sometimes an error string with a
// hostname in it — goal.md §8.5 keeps all of that on the machine. What
// travels is the part of the request that was already public: which
// package was being asked about, and which symbol, if the caller named
// one. That is enough to say WHAT is wanted without saying who wants it or
// what they are building.
type wantedReport struct {
	SchemaVersion int      `json:"schemaVersion"`
	Epoch         string   `json:"epoch"`
	AnonID        string   `json:"anonId"`
	Packages      []string `json:"packages"`
	Symbols       []string `json:"symbols,omitempty"`
}

// handleWanted implements POST /v1/wanted: count one anonymous report that
// the network had no answer for these packages.
//
// Anonymous and unauthenticated, like evidence — accounts are not the
// answer to abuse (§8.6). What keeps it honest is the dedup ledger: one
// reporter counts once per epoch per row, so asking the same thing all
// afternoon is one data point and nobody can manufacture a ranking by
// repeating themselves.
func (a *api) handleWanted(w http.ResponseWriter, r *http.Request) {
	var req wantedReport
	if !readJSON(w, r, 1<<18, &req) {
		return
	}
	if req.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "wanted report schemaVersion must be 1")
		return
	}
	if req.Epoch == "" || req.AnonID == "" {
		writeErr(w, http.StatusBadRequest, "wanted report requires epoch and anonId")
		return
	}
	if len(req.Packages) == 0 {
		writeErr(w, http.StatusBadRequest, "wanted report requires at least one package")
		return
	}

	// A symbol is only meaningful attached to its package, and the report
	// does not say which symbol went with which purl. Rather than invent a
	// pairing, the first symbol is recorded against every package in the
	// report only when there is exactly one package — the case where the
	// pairing is not a guess. Otherwise the row is the package alone.
	symbol := ""
	if len(req.Packages) == 1 && len(req.Symbols) > 0 {
		symbol = strings.TrimSpace(req.Symbols[0])
	}

	seen := map[string]bool{}
	rows := make([]serverstore.WantedRow, 0, len(req.Packages))
	for _, ps := range req.Packages {
		p, err := domain.ParsePURL(ps)
		if err != nil {
			continue // an unparseable purl is dropped, never guessed at
		}
		key := p.Ecosystem + "/" + p.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, serverstore.WantedRow{
			Ecosystem: p.Ecosystem, Name: p.Name, Symbol: symbol,
		})
		if len(rows) >= maxWantedPerReport {
			break
		}
	}
	if len(rows) == 0 {
		writeErr(w, http.StatusBadRequest, "no parseable package in the report")
		return
	}

	if err := a.d.Store.RecordWanted(r.Context(), req.Epoch, req.AnonID, rows); err != nil {
		writeErr(w, http.StatusInternalServerError, "recording the report failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "counted": len(rows)})
}
