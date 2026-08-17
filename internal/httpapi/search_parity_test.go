package httpapi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	localsearch "github.com/r2cuerdame/codesamplex/internal/search"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func TestLocalAndServerSymbolProvenanceParity(t *testing.T) {
	npm := serverExplicitNode24Env()
	pypi := domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "pypi", OS: "windows", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip"}.Normalize()
	tests := []struct {
		name       string
		goal       string
		purl       string
		symbol     string
		sampleEnv  domain.EnvironmentFingerprint
		request    domain.SearchRequest
		wantMiss   bool
		secondPeer bool
	}{
		{
			name: "first miss second declared match", goal: "invoke tools list over json rpc", purl: "pkg:npm/mcp-fixture@1.0.0",
			symbol: "tools/list", sampleEnv: npm,
			request: domain.SearchRequest{Query: "invoke tools list over json rpc", Symbols: []string{"process.stdin", "tools/list"}},
		},
		{
			name: "context only ranks without exclusion", goal: "serialize a widget transport", purl: "pkg:npm/widget@1.0.0",
			symbol: "widget.serialize", sampleEnv: npm,
			request:    domain.SearchRequest{Query: "serialize a widget transport", ContextSymbols: []string{"widget.serialize"}},
			secondPeer: true,
		},
		{
			name: "context miss does not exclude", goal: "serialize a widget transport", purl: "pkg:npm/widget@1.0.0",
			symbol: "widget.serialize", sampleEnv: npm,
			request: domain.SearchRequest{Query: "serialize a widget transport", ContextSymbols: []string{"ambient.unrelated"}},
		},
		{
			name: "generic tokens stay weak", goal: "keep pydantic secrets out of encoded output", purl: "pkg:pypi/pydantic@2.12.5",
			symbol: "pydantic.SecretStr", sampleEnv: pypi,
			request:  domain.SearchRequest{Query: "build json server process model protocol tools", ContextSymbols: []string{"json", "server", "process"}},
			wantMiss: true,
		},
		{
			name: "full dotted symbol survives generic pieces", goal: "call the full model server process symbol", purl: "pkg:npm/dotted@1.0.0",
			symbol: "model.server.node.json.process", sampleEnv: npm,
			request: domain.SearchRequest{Query: "call model.server.node.json.process directly", Symbols: []string{"model.server.node.json.process"}},
		},
		{
			name: "explicit ecosystem gate", goal: "freeze time in a python test", purl: "pkg:pypi/freezegun@1.5.5",
			symbol: "freezegun.freeze_time", sampleEnv: pypi,
			request: domain.SearchRequest{Query: "freeze time in a python test"}, wantMiss: true,
		},
		{
			name: "context ecosystem may soften", goal: "freeze time in a python test", purl: "pkg:pypi/freezegun@1.5.5",
			symbol: "freezegun.freeze_time", sampleEnv: pypi,
			request: domain.SearchRequest{Query: "freeze time in a python test", EnvironmentProvenance: domain.SearchProvenanceContext},
		},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			targetID := "sha256:" + strings.Repeat(string("1234567"[i]), 64)
			manifest := parityManifest(tc.goal, tc.purl, tc.symbol, tc.sampleEnv)

			db, err := localdb.Open(filepath.Join(t.TempDir(), "csx.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := localsearch.SeedSampleDoc(context.Background(), db, manifest, targetID, "CROSS_PASS"); err != nil {
				t.Fatal(err)
			}

			srv, store, _ := newTestServer(t, nil)
			saveSearchFixture(t, store, targetID, tc.goal, tc.purl, tc.symbol, tc.sampleEnv)
			if tc.secondPeer {
				// Sorts before targetID, so the context match must earn the top
				// slot on both paths rather than winning a stable-sort tie.
				otherID := "sha256:" + strings.Repeat("0", 64)
				other := parityManifest(tc.goal, tc.purl, "other.symbol", tc.sampleEnv)
				if err := localsearch.SeedSampleDoc(context.Background(), db, other, otherID, "CROSS_PASS"); err != nil {
					t.Fatal(err)
				}
				saveSearchFixture(t, store, otherID, tc.goal, tc.purl, "other.symbol", tc.sampleEnv)
			}

			req := tc.request
			req.SchemaVersion = 2
			req.Environment = npm
			if req.EnvironmentProvenance == "" {
				req.EnvironmentProvenance = domain.SearchProvenanceExplicit
			}
			if len(req.Symbols) > 0 {
				req.SymbolProvenance = domain.SearchProvenanceExplicit
			}
			local := (localsearch.Engine{DB: db}).Search(t.Context(), req)
			var server domain.SearchResponse
			postJSON(t, srv.URL+"/v2/search", req, &server)
			if local.Miss != server.Miss || local.Miss != tc.wantMiss {
				t.Fatalf("miss parity local=%v server=%v want=%v; local=%v server=%v",
					local.Miss, server.Miss, tc.wantMiss, parityIDs(local.Results), parityIDs(server.Results))
			}
			if !tc.wantMiss {
				if len(local.Results) == 0 || len(server.Results) == 0 ||
					local.Results[0].SampleID != targetID || server.Results[0].SampleID != targetID {
					t.Fatalf("top parity local=%v server=%v want=%s", parityIDs(local.Results), parityIDs(server.Results), targetID)
				}
			}
		})
	}
}

func parityManifest(goal, purl, symbol string, env domain.EnvironmentFingerprint) domain.SampleManifest {
	return domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: goal,
			Packages: []string{purl}, Symbols: []string{symbol}, Contract: []string{"asserts documented behavior"}},
		Packages: []string{purl}, Symbols: []string{symbol}, Environment: env,
		License: "MIT-0", ContractCommand: []string{"test"}, VerifierAdapter: "parity@1",
	}
}

func parityIDs(results []domain.SearchResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.SampleID)
	}
	return ids
}
