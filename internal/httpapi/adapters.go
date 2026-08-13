package httpapi

import (
	_ "embed"
	"net/http"
)

// adaptersJSON is a byte-for-byte copy of schemas/v1/adapters.json — the
// published capability matrix (goal.md §13.1). A test asserts equality with
// the schema file so the two can never drift.
//
//go:embed adapters.json
var adaptersJSON []byte

// handleAdapters implements GET /v1/adapters.
func (a *api) handleAdapters(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(adaptersJSON)
}
