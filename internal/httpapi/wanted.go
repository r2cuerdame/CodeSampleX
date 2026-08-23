package httpapi

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// maxWantedPerReport bounds one report. A caller with a large dependency
// tree could otherwise file hundreds of rows from a single miss and cheaply
// inflate write work; the packages that mattered to the question are the
// first few.
const maxWantedPerReport = 10
const maxWantedBatchReports = 20
const maxWantedEpochAgeDays = 30

var wantedEcosystems = map[string]bool{
	"npm": true, "pypi": true, "cargo": true, "golang": true,
	"gem": true, "composer": true, "hex": true, "pub": true,
	// Maven supports verification-only A4 samples. Wanted also accepts it so
	// unmet Java demand is visible despite the deliberate lack of an A0-A2
	// local project scanner; /adapters remains the capability source of truth.
	"maven": true,
	// generic is limited below to domain.IsWantedTarget's fixed public
	// engine/SDK vocabulary; arbitrary generic purls are rejected.
	"generic": true,
}

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
	// OS is the platform the miss happened on, and it is optional: a report
	// without one is a question about the package rather than about a
	// platform, and any verifier that can run it may answer.
	//
	// It is restricted to wantedReportOSes rather than accepting whatever the
	// client calls itself. A free-text environment string attached to a
	// stable anonymous id is a fingerprint, and the coarse platform name is
	// the only part of it the work queue can act on.
	OS string `json:"os,omitempty"`
}

// wantedReportOSes is the entire vocabulary a report may name. It matches the
// platforms verification can actually target; anything else is refused rather
// than stored and quietly ignored.
var wantedReportOSes = map[string]bool{"linux": true, "windows": true, "darwin": true}

type wantedBatch struct {
	SchemaVersion int            `json:"schemaVersion"`
	Reports       []wantedReport `json:"reports"`
}

type wantedListItem struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Asks      int64  `json:"asks"`
}

// handleWantedList exposes the actionable, privacy-safe request queue.  It
// contains only public package coordinates and symbols; the caller's prose,
// project and identity never entered the wanted table in the first place.
func (a *api) handleWantedList(w http.ResponseWriter, r *http.Request) {
	// The contributor producer asks for this feed directly.  Keep enough
	// headroom for a useful batch while the human /wanted page stays at its
	// deliberately shorter presentation limit.
	rows, err := a.d.Store.TopWanted(r.Context(), 200)
	if err != nil {
		writeStoreErr(w, err, http.StatusInternalServerError, "listing wanted requests failed")
		return
	}
	items := make([]wantedListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, wantedListItem{
			Ecosystem: row.Ecosystem,
			Name:      row.Name,
			Version:   row.Version,
			Symbol:    row.Symbol,
			Asks:      row.Asks,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1,
		"generatedAt":   a.now().UTC(),
		"items":         items,
	})
}

// handleWanted implements POST /v1/wanted: count one anonymous report that
// the network had no answer for these packages.
//
// Anonymous and unauthenticated, like evidence — accounts are not the
// answer to abuse (§8.6). The dedup ledger makes an identical reporter count
// once per epoch per row, while strict envelopes, public-version checks and
// address budgets limit cheap repetition. Anonymous ids do not prove unique
// humans, so these controls reduce ranking manipulation rather than claiming
// to make it impossible.
func (a *api) handleWanted(w http.ResponseWriter, r *http.Request) {
	var req wantedReport
	if !readJSON(w, r, 1<<18, &req) {
		return
	}
	rows, err := rowsForWantedReport(req, a.now())
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	publicness, lookups := map[string]string{}, 0
	rows, err = a.publicWantedRows(r.Context(), rows, publicness, &lookups)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.d.Store.RecordWanted(r.Context(), req.Epoch, req.AnonID, rows); err != nil {
		writeErr(w, http.StatusInternalServerError, "recording the report failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "accepted", "counted": len(rows)})
}

// handleWantedBatch drains many durable local queue rows in one write-budget
// unit.  Reports remain individually deduplicated by (epoch, anonId), so a
// client may safely retry the whole batch after a lost response.
func (a *api) handleWantedBatch(w http.ResponseWriter, r *http.Request) {
	var batch wantedBatch
	if !readJSON(w, r, 1<<20, &batch) {
		return
	}
	if batch.SchemaVersion != 1 {
		writeErr(w, http.StatusBadRequest, "wanted batch schemaVersion must be 1")
		return
	}
	if len(batch.Reports) == 0 || len(batch.Reports) > maxWantedBatchReports {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("wanted batch requires 1..%d reports", maxWantedBatchReports))
		return
	}
	// Validate the complete envelope before writing any row. A malformed
	// report must not turn a 400 response into a partially applied batch;
	// transport/server failures can still be retried safely through the
	// per-report dedup ledger.
	parsed := make([][]serverstore.WantedRow, len(batch.Reports))
	publicness, lookups := map[string]string{}, 0
	for i, req := range batch.Reports {
		rows, err := rowsForWantedReport(req, a.now())
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("wanted report %d: %v", i, err))
			return
		}
		rows, err = a.publicWantedRows(r.Context(), rows, publicness, &lookups)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("wanted report %d: %v", i, err))
			return
		}
		parsed[i] = rows
	}
	total := 0
	submissions := make([]serverstore.WantedSubmission, 0, len(batch.Reports))
	for i, req := range batch.Reports {
		rows := parsed[i]
		submissions = append(submissions, serverstore.WantedSubmission{Epoch: req.Epoch, AnonID: req.AnonID, Rows: rows})
		total += len(rows)
	}
	if err := a.d.Store.RecordWantedBatch(r.Context(), submissions); err != nil {
		writeErr(w, http.StatusInternalServerError, "recording the reports failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "accepted", "reports": len(batch.Reports), "counted": total,
	})
}

func rowsForWantedReport(req wantedReport, now time.Time) ([]serverstore.WantedRow, error) {
	if req.SchemaVersion != 1 {
		return nil, fmt.Errorf("wanted report schemaVersion must be 1")
	}
	parsedDay, err := time.Parse("2006-01-02", req.Epoch)
	if err != nil || parsedDay.Format("2006-01-02") != req.Epoch {
		return nil, fmt.Errorf("wanted report epoch must be YYYY-MM-DD")
	}
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if parsedDay.Before(today.AddDate(0, 0, -maxWantedEpochAgeDays)) || parsedDay.After(today.AddDate(0, 0, 1)) {
		return nil, fmt.Errorf("wanted report epoch is outside the accepted backlog window")
	}
	if len(req.AnonID) != 16 {
		return nil, fmt.Errorf("wanted report anonId must be 16 hexadecimal characters")
	}
	if _, err := hex.DecodeString(req.AnonID); err != nil {
		return nil, fmt.Errorf("wanted report anonId must be 16 hexadecimal characters")
	}
	reportedOS := strings.ToLower(strings.TrimSpace(req.OS))
	if reportedOS != "" && !wantedReportOSes[reportedOS] {
		return nil, fmt.Errorf("wanted report os must be one of linux, windows, darwin")
	}
	if len(req.Packages) == 0 || len(req.Packages) > maxWantedPerReport {
		return nil, fmt.Errorf("wanted report requires 1..%d packages", maxWantedPerReport)
	}
	if len(req.Symbols) > maxWantedPerReport {
		return nil, fmt.Errorf("wanted report allows at most %d symbols", maxWantedPerReport)
	}

	seenPackages := map[string]bool{}
	packages := make([]domain.PURL, 0, len(req.Packages))
	for _, ps := range req.Packages {
		if len(ps) == 0 || len(ps) > 512 {
			return nil, fmt.Errorf("package purl length must be 1..512 bytes")
		}
		p, err := domain.ParsePURL(ps)
		if err != nil {
			return nil, fmt.Errorf("package purl is invalid")
		}
		validCoordinate := domain.IsWantedTarget(p) ||
			(wantedEcosystems[p.Ecosystem] && registry.ValidPackageName(p.Ecosystem, p.Name))
		if !validCoordinate ||
			len(p.Name) > 256 ||
			len(p.Version) > 128 || !domain.ConcreteResolvedVersion(p.Version) {
			return nil, fmt.Errorf("package must name a supported ecosystem and concrete release")
		}
		key := p.Ecosystem + "/" + p.Name + "@" + p.Version
		if seenPackages[key] {
			continue
		}
		seenPackages[key] = true
		packages = append(packages, p)
		if len(packages) >= maxWantedPerReport {
			break
		}
	}
	if len(packages) == 0 {
		return nil, fmt.Errorf("no parseable package in the report")
	}

	// Symbols are global in the v1 search request, so attaching them to
	// multiple packages would invent a relationship the client did not
	// send.  A one-package question is unambiguous, however, and every named
	// symbol is a separate request rather than silently discarding all but
	// the first one.
	symbols := []string{""}
	if len(packages) == 1 {
		seenSymbols := map[string]bool{}
		symbols = symbols[:0]
		for _, raw := range req.Symbols {
			symbol := strings.TrimSpace(raw)
			if len(symbol) > 256 {
				return nil, fmt.Errorf("symbol length must be at most 256 bytes")
			}
			for _, r := range symbol {
				if unicode.IsControl(r) {
					return nil, fmt.Errorf("symbol may not contain control characters")
				}
			}
			if symbol == "" || seenSymbols[symbol] {
				continue
			}
			seenSymbols[symbol] = true
			symbols = append(symbols, symbol)
			if len(symbols) >= maxWantedPerReport {
				break
			}
		}
		if len(symbols) == 0 {
			symbols = append(symbols, "")
		}
	}

	rows := make([]serverstore.WantedRow, 0, min(maxWantedPerReport, len(packages)*len(symbols)))
	for _, p := range packages {
		for _, symbol := range symbols {
			rows = append(rows, serverstore.WantedRow{
				Ecosystem: p.Ecosystem, Name: p.Name, Version: p.Version, Symbol: symbol,
				TargetOS: reportedOS,
			})
			if len(rows) >= maxWantedPerReport {
				break
			}
		}
		if len(rows) >= maxWantedPerReport {
			break
		}
	}

	return rows, nil
}

// publicWantedRows is the same fail-closed publicness boundary used by
// evidence ingest. Parsing a PURL proves only its syntax; it says nothing
// about whether the name/version exists on a public registry.
func (a *api) publicWantedRows(ctx context.Context, rows []serverstore.WantedRow,
	cache map[string]string, lookups *int) ([]serverstore.WantedRow, error) {
	if a.trustMode() {
		return rows, nil
	}
	out := make([]serverstore.WantedRow, 0, len(rows))
	for _, row := range rows {
		p := domain.PURL{Ecosystem: row.Ecosystem, Name: row.Name, Version: row.Version}
		if domain.IsWantedTarget(p) {
			out = append(out, row)
			continue
		}
		if a.d.Checker == nil {
			return nil, fmt.Errorf("public package check unavailable")
		}
		key := p.String()
		status, ok := cache[key]
		if !ok {
			if *lookups >= maxRegistryLookupsPerRequest {
				return nil, fmt.Errorf("too many distinct packages for one request")
			}
			*lookups++
			status = a.d.Checker.Check(ctx, p)
			cache[key] = status
		}
		if status == scanner.PublicnessPublic {
			out = append(out, row)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no confirmed public package in the report")
	}
	return out, nil
}
