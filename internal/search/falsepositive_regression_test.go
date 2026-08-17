package search

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

const mcpStdioQuery = "implement a Model Context Protocol server over stdio in plain Node.js ESM without the official SDK: initialize handshake, tools/list, tools/call newline-delimited JSON-RPC"

func explicitNode24Env() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "24.13", Language: "javascript",
		ModuleSystem: "esm", PackageManager: "npm", ExecutionContext: "node",
	}.Normalize()
}

func seedFalsePositiveCorpus(t *testing.T) Engine {
	t.Helper()
	db := openDB(t)
	ctx := context.Background()
	fixtures := []struct {
		id, goal, purl, symbol string
		env                    domain.EnvironmentFingerprint
	}{
		{"sha256:pydantic-secretstr", "Keep model secrets out of JSON with Pydantic SecretStr",
			"pkg:pypi/pydantic@2.12.5", "pydantic.SecretStr",
			domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "pypi", OS: "windows", Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip"}.Normalize()},
		{"sha256:go-http-protocols", "Configure HTTP server protocols in Go",
			"pkg:golang/golang.org/x/net@v0.47.0", "net/http.Protocols",
			domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang", OS: "windows", Runtime: "go", RuntimeVersion: "1.25", Language: "go", PackageManager: "gomod"}.Normalize()},
		{"sha256:react-dom", "Render a React model into a DOM node on the server",
			"pkg:npm/react-dom@19.2.3", "ReactDOM.createPortal", explicitNode24Env()},
	}
	for _, fixture := range fixtures {
		m := mkManifest(fixture.goal, []string{fixture.purl}, fixture.env, fixture.symbol)
		if err := SeedSampleDoc(ctx, db, m, fixture.id, "CROSS_PASS"); err != nil {
			t.Fatal(err)
		}
	}
	return Engine{DB: db}
}

func TestPackageLessDeclaredSymbolsRejectTheReproducedFalsePositivesLocally(t *testing.T) {
	e := seedFalsePositiveCorpus(t)
	resp := e.Search(context.Background(), domain.SearchRequest{
		SchemaVersion: 2, Query: mcpStdioQuery,
		Symbols:               []string{"process.stdin", "JSON-RPC", "tools/list", "tools/call"},
		SymbolProvenance:      domain.SearchProvenanceExplicit,
		Environment:           explicitNode24Env(),
		EnvironmentProvenance: domain.SearchProvenanceExplicit,
	})
	if !resp.Miss || len(resp.Results) != 0 {
		t.Fatalf("got miss=%v results=%v, want NO_SAFE_MATCH", resp.Miss, resultIDs(resp.Results))
	}
}

func TestDeclaredSymbolHitsRemainReachableLocally(t *testing.T) {
	for _, tc := range []struct {
		name, query, symbol string
	}{
		{"stdio", mcpStdioQuery, "process.stdin"},
		{"react renderToString", "render a react component to an html string", "renderToString"},
		{"full dotted", "call model.server.node.json.process directly", "model.server.node.json.process"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			m := mkManifest(tc.query, []string{"pkg:npm/verified-example@1.0.0"}, explicitNode24Env(), tc.symbol)
			if err := SeedSampleDoc(context.Background(), db, m, "sha256:"+strings.ReplaceAll(tc.name, " ", "-"), "CROSS_PASS"); err != nil {
				t.Fatal(err)
			}
			resp := (Engine{DB: db}).Search(context.Background(), domain.SearchRequest{
				SchemaVersion: 2, Query: tc.query, Symbols: []string{tc.symbol},
				SymbolProvenance: domain.SearchProvenanceExplicit, Environment: explicitNode24Env(),
				EnvironmentProvenance: domain.SearchProvenanceExplicit,
			})
			if resp.Miss || len(resp.Results) == 0 {
				t.Fatal("a full declared-symbol identity should remain a hit")
			}
		})
	}
}

func TestExplicitEnvironmentSurvivesCrossEcosystemGrading(t *testing.T) {
	req := explicitNode24Env()
	sample := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "windows", Arch: "amd64",
		Runtime: "python", RuntimeVersion: "3.13", Language: "python", PackageManager: "pip",
	}.Normalize()
	asked := envAskedAbout(req, "pypi", false)
	if asked.Ecosystem != "npm" || asked.Runtime != "node" || asked.Language != "javascript" ||
		asked.ModuleSystem != "esm" || asked.PackageManager != "npm" {
		t.Fatalf("explicit dimensions were erased: %+v", asked)
	}
	dims := compareEnv(asked, sample, "pypi")
	grade, _ := buildGrade(relUnspecified, dims, compareContext(asked, sample), false)
	_, different := buildDelta(relUnspecified, domain.PURL{}, domain.PURL{}, dims, compareContext(asked, sample))
	if grade != domain.GradeReferenceOnly {
		t.Fatalf("grade=%s, want REFERENCE_ONLY for explicit npm vs pypi", grade)
	}
	joined := strings.Join(different, "\n")
	for _, want := range []string{"ecosystem pypi", "ecosystem npm", "python", "node", "pip", "npm"} {
		if !strings.Contains(strings.ToLower(joined), want) {
			t.Errorf("Different=%v, missing %q", different, want)
		}
	}
}

func TestInferredEnvironmentKeepsLegacyCrossEcosystemSoftening(t *testing.T) {
	asked := envAskedAbout(explicitNode24Env(), "pypi", true)
	if asked.Ecosystem != "" || asked.Runtime != "" || asked.Language != "" ||
		asked.ModuleSystem != "" || asked.PackageManager != "" {
		t.Fatalf("inferred project dimensions were not softened: %+v", asked)
	}
	if asked.OS != "windows" || asked.Arch != "amd64" {
		t.Fatalf("machine dimensions should remain: %+v", asked)
	}
}

func resultIDs(results []domain.SearchResult) []string {
	out := make([]string, 0, len(results))
	for _, result := range results {
		out = append(out, result.SampleID)
	}
	return out
}
