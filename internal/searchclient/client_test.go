package searchclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestV2ClientFallsBackToStrictOldServer(t *testing.T) {
	var legacy map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/search":
			http.NotFound(w, r)
		case "/v1/search":
			if err := json.NewDecoder(r.Body).Decode(&legacy); err != nil {
				t.Error(err)
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"schemaVersion":1,"results":[],"miss":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	response, err := (Client{BaseURL: server.URL}).Search(context.Background(), domain.SearchRequest{
		SchemaVersion: 2, Query: "post json with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Symbols: []string{"axios.post"},
		ContextSymbols: []string{"ambient.symbol"}, SymbolProvenance: domain.SearchProvenanceExplicit,
		Environment:           domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64"},
		EnvironmentProvenance: domain.SearchProvenanceExplicit,
		ErrorFingerprints:     []string{"sha256:v2-only"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.SchemaVersion != 1 || !response.Miss {
		t.Fatalf("old response not accepted: %+v", response)
	}
	allowed := map[string]bool{
		"schemaVersion": true, "query": true, "packages": true, "symbols": true,
		"environment": true, "errorFingerprint": true, "errorCode": true, "limit": true,
	}
	for key := range legacy {
		if !allowed[key] {
			t.Fatalf("strict old request validator rejects leaked v2 key %q: %#v", key, legacy)
		}
	}
	if legacy["schemaVersion"] != float64(1) {
		t.Fatalf("fallback did not downgrade schemaVersion: %#v", legacy)
	}
}
