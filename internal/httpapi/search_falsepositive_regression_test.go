package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const serverMCPStdioQuery = "implement a Model Context Protocol server over stdio in plain Node.js ESM without the official SDK: initialize handshake, tools/list, tools/call newline-delimited JSON-RPC"

func serverExplicitNode24Env() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "24.13", Language: "javascript",
		ModuleSystem: "esm", PackageManager: "npm", ExecutionContext: "node",
	}.Normalize()
}

func saveSearchFixture(t *testing.T, store *serverstore.Fake, id, goal, purl, symbol string,
	env domain.EnvironmentFingerprint) {
	t.Helper()
	m := domain.SampleManifest{
		SchemaVersion: 1,
		Case: domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: goal,
			Packages: []string{purl}, Symbols: []string{symbol}, Contract: []string{"asserts documented behavior"}},
		Packages: []string{purl}, Symbols: []string{symbol}, Environment: env,
		License: "MIT-0", ContractCommand: []string{"test"}, VerifierAdapter: "fixture@1",
	}
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(m)), Status: "CROSS_PASS",
		License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPackageLessDeclaredSymbolsRejectTheReproducedFalsePositivesOnServer(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	fixtures := []struct {
		id, goal, purl, symbol string
		env                    domain.EnvironmentFingerprint
	}{
		{"sha256:" + strings.Repeat("31", 32), "Keep model secrets out of JSON with Pydantic SecretStr", "pkg:pypi/pydantic@2.12.5", "pydantic.SecretStr",
			domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "pypi", Runtime: "python", Language: "python", PackageManager: "pip"}.Normalize()},
		{"sha256:" + strings.Repeat("32", 32), "Configure HTTP server protocols in Go", "pkg:golang/golang.org/x/net@v0.47.0", "net/http.Protocols",
			domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", Runtime: "go", Language: "go", PackageManager: "gomod"}.Normalize()},
		{"sha256:" + strings.Repeat("33", 32), "Render a React model into a DOM node on the server", "pkg:npm/react-dom@19.2.3", "ReactDOM.createPortal", serverExplicitNode24Env()},
	}
	for _, fixture := range fixtures {
		saveSearchFixture(t, store, fixture.id, fixture.goal, fixture.purl, fixture.symbol, fixture.env)
	}
	var out domain.SearchResponse
	postJSON(t, srv.URL+"/v2/search", domain.SearchRequest{
		SchemaVersion: 2, Query: serverMCPStdioQuery,
		Symbols:               []string{"process.stdin", "JSON-RPC", "tools/list", "tools/call"},
		SymbolProvenance:      domain.SearchProvenanceExplicit,
		Environment:           serverExplicitNode24Env(),
		EnvironmentProvenance: domain.SearchProvenanceExplicit,
	}, &out)
	if !out.Miss || len(out.Results) != 0 {
		t.Fatalf("got miss=%v results=%d, want NO_SAFE_MATCH", out.Miss, len(out.Results))
	}
}

func TestDeclaredSymbolHitsRemainReachableOnServer(t *testing.T) {
	for _, tc := range []struct{ name, query, symbol string }{
		{"stdio", serverMCPStdioQuery, "process.stdin"},
		{"react", "render a react component to an html string", "renderToString"},
		{"dotted", "call model.server.node.json.process directly", "model.server.node.json.process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			saveSearchFixture(t, store, "sha256:"+strings.Repeat(tc.name[:1], 64), tc.query,
				"pkg:npm/verified-example@1.0.0", tc.symbol, serverExplicitNode24Env())
			var out domain.SearchResponse
			postJSON(t, srv.URL+"/v2/search", domain.SearchRequest{
				SchemaVersion: 2, Query: tc.query, Symbols: []string{tc.symbol},
				SymbolProvenance: domain.SearchProvenanceExplicit, Environment: serverExplicitNode24Env(),
				EnvironmentProvenance: domain.SearchProvenanceExplicit,
			}, &out)
			if out.Miss || len(out.Results) == 0 {
				t.Fatal("a full declared-symbol identity should remain a hit")
			}
		})
	}
}

func TestServerExplicitNPMVsPyPIReportsTheDifference(t *testing.T) {
	req := serverExplicitNode24Env()
	sample := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "windows", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip",
	}.Normalize()
	delta := envDelta(req, sample, domain.PURL{Ecosystem: "pypi", Name: "pydantic", Version: "2.12.5"}, "")
	if delta.grade != domain.GradeReferenceOnly {
		t.Fatalf("grade=%s, want REFERENCE_ONLY", delta.grade)
	}
	joined := strings.ToLower(strings.Join(delta.different, "\n"))
	for _, want := range []string{"ecosystem npm", "sample: pypi", "node", "python", "npm", "pip"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Different=%v, missing %q", delta.different, want)
		}
	}
}
