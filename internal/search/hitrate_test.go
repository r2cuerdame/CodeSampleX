package search

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// This file is the answer to a question nobody could answer before it
// existed: how often does csx actually answer?
//
// Every search defect found so far was found by hand, one query at a time,
// and each fix was argued rather than measured. The worst of them — the
// caller's lockfile being read as the question — took a correct answer from
// 0.27 to 0.105 and turned four working queries into NO_SAFE_MATCH, and
// nothing in the suite noticed, because every existing test seeds exactly
// the sample it then asks for.
//
// The corpus below is deliberately awkward: the questions are worded the
// way a developer would word them, not the way the goals are written, and
// each one runs from inside a project that has nothing to do with it. Two
// numbers matter and they are reported separately:
//
//	hits    — the right sample came back
//	wrong   — a sample came back and it was the wrong one
//
// A wrong answer is worse than a miss (goal.md §3.8), so `wrong` is a hard
// failure at any count, while `hits` is a floor that may be raised as the
// engine improves and must never be quietly lowered to make a change pass.

// corpusEntry is one question and the sample that should answer it.
type corpusEntry struct {
	query string
	want  string // sample ID
	// from is the project the question is asked from — the caller's real
	// environment, which in practice has nothing to do with the question.
	from domain.EnvironmentFingerprint
	// tree is the caller's dependency tree, filled in automatically by the
	// CLI from a lockfile. It is context; it must never decide the answer.
	tree []string
}

func goEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "golang", OS: "windows", OSVersionBucket: "11",
		Arch: "x64", Runtime: "go", RuntimeVersion: "1.26.0",
		Language: "go", LanguageVersion: "1.26.0", PackageManager: "gomod",
	}
}

func pyEnv() domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "pypi", OS: "linux", OSVersionBucket: "debian",
		Arch: "x64", Runtime: "python", RuntimeVersion: "3.13.2",
		Language: "python", LanguageVersion: "3.13.2", PackageManager: "pip",
	}
}

func alpineEnv(ecosystem, runtime, version, language string) domain.EnvironmentFingerprint {
	return domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: ecosystem, OS: "linux", OSVersionBucket: "alpine",
		Arch: "x64", Runtime: runtime, RuntimeVersion: version, Language: language,
		LanguageVersion: version, Virtualization: "container", ContainerRuntime: "docker",
		Libc: "musl",
	}
}

// seedCorpus plants samples shaped like the ones the network really holds:
// verified in an alpine container, so their environment never equals the
// caller's.
func seedCorpus(t *testing.T) (*testEngine, []corpusEntry) {
	t.Helper()
	db := openDB(t)
	ctx := context.Background()

	type seed struct {
		id      string
		goal    string
		pkgs    []string
		env     domain.EnvironmentFingerprint
		symbols []string
	}
	seeds := []seed{
		{"sha256:jwt1",
			"Verify a JWT with golang-jwt v5 and tell an expired token apart from a forged one",
			[]string{"pkg:golang/github.com/golang-jwt/jwt/v5@v5.3.0"},
			alpineEnv("golang", "go", "1.26.0", "go"),
			[]string{"ParseWithClaims", "ErrTokenExpired"}},
		{"sha256:freeze1",
			"Freeze the clock in a Python test with freezegun so datetime, time.time and time.monotonic agree",
			[]string{"pkg:pypi/freezegun@1.6.1"},
			alpineEnv("pypi", "python", "3.13.2", "python"),
			[]string{"freeze_time"}},
		{"sha256:axum1",
			"Test axum handlers and routing without binding a port by driving the Router through tower's oneshot",
			[]string{"pkg:cargo/axum@0.8.7"},
			alpineEnv("cargo", "rust", "1.91.0", "rust"),
			[]string{"ServiceExt::oneshot"}},
		{"sha256:ecto1",
			"Validate a plain map with Ecto.Changeset alone: an embedded_schema, no Repo and no database",
			[]string{"pkg:hex/ecto@3.13.4"},
			alpineEnv("hex", "elixir", "1.19.0", "elixir"),
			[]string{"Ecto.Changeset.cast", "embedded_schema"}},
		{"sha256:gorm1",
			"Run GORM on SQLite with CGO_ENABLED=0, past go-sqlite3's runtime stub error",
			[]string{"pkg:golang/gorm.io/gorm@v1.31.2"},
			alpineEnv("golang", "go", "1.26.0", "go"),
			[]string{"gorm.Open"}},
		{"sha256:uuid1",
			"Generate, parse and validate a UUID in Go with google/uuid",
			[]string{"pkg:golang/github.com/google/uuid@v1.6.0"},
			alpineEnv("golang", "go", "1.26.0", "go"),
			[]string{"uuid.Parse", "uuid.Validate"}},
		{"sha256:react1",
			"Choose between renderToString and renderToStaticMarkup without breaking hydration",
			[]string{"pkg:npm/react-dom@19.2.8"},
			alpineEnv("npm", "node", "22.18.1", "javascript"),
			[]string{"renderToStaticMarkup"}},
	}
	for _, s := range seeds {
		m := mkManifest(s.goal, s.pkgs, s.env, s.symbols...)
		if err := SeedSampleDoc(ctx, db, m, s.id, "CROSS_PASS"); err != nil {
			t.Fatalf("seed %s: %v", s.id, err)
		}
	}

	goTree := []string{
		"pkg:golang/github.com/jackc/pgx/v5@v5.7.6",
		"pkg:golang/github.com/google/uuid@v1.6.0",
		"pkg:golang/gorm.io/gorm@v1.31.2",
	}
	pyTree := []string{"pkg:pypi/fastapi@0.141.1", "pkg:pypi/pydantic@2.12.4"}

	corpus := []corpusEntry{
		// Asked from a Go project about a Go library it does not have yet.
		{"verify a JWT with golang-jwt v5", "sha256:jwt1", goEnv(), goTree},
		{"how do I tell an expired token from a bad signature", "sha256:jwt1", goEnv(), goTree},
		// Asked from a Go project about other ecosystems entirely.
		{"freeze the clock in a python test", "sha256:freeze1", goEnv(), goTree},
		{"test axum handlers without binding a port", "sha256:axum1", goEnv(), goTree},
		{"validate a map with Ecto.Changeset without a Repo", "sha256:ecto1", goEnv(), goTree},
		// Asked from a Python project about a Go library.
		{"run GORM on SQLite with CGO_ENABLED=0", "sha256:gorm1", pyEnv(), pyTree},
		// Worded nothing like the goal; the package name is the only link.
		{"render a react component to an html string", "sha256:react1", goEnv(), goTree},
		// About a package that IS in the caller's tree.
		{"parse and validate a uuid in go", "sha256:uuid1", goEnv(), goTree},
	}
	return &testEngine{Engine{DB: db}, ctx}, corpus
}

type testEngine struct {
	Engine
	ctx context.Context
}

func (e *testEngine) ask(c corpusEntry) domain.SearchResponse {
	return e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion:   1,
		Query:           c.query,
		ProjectPackages: c.tree,
		Environment:     c.from,
		Limit:           3,
	})
}

// hitFloor is the number of corpus questions that must be answered
// correctly. Raise it when the engine improves; never lower it to make a
// change pass — a change that lowers it is a regression with an excuse.
const hitFloor = 8

func TestHitRateOnRealisticQuestions(t *testing.T) {
	e, corpus := seedCorpus(t)

	var hits, misses int
	var wrong []string
	var report strings.Builder
	for _, c := range corpus {
		resp := e.ask(c)
		switch {
		case resp.Miss || len(resp.Results) == 0:
			misses++
			fmt.Fprintf(&report, "  MISS   %s\n", c.query)
		case resp.Results[0].SampleID == c.want:
			hits++
			fmt.Fprintf(&report, "  hit    %-12s %s\n", resp.Results[0].Grade, c.query)
		default:
			wrong = append(wrong, fmt.Sprintf("%q answered with %s (wanted %s)",
				c.query, resp.Results[0].SampleID, c.want))
			fmt.Fprintf(&report, "  WRONG  %-12s %s -> %s\n",
				resp.Results[0].Grade, c.query, resp.Results[0].SampleID)
		}
	}
	t.Logf("hit rate %d/%d (miss %d, wrong %d)\n%s",
		hits, len(corpus), misses, len(wrong), report.String())

	// A wrong answer is worse than a miss, so any count fails.
	for _, w := range wrong {
		t.Errorf("wrong answer: %s", w)
	}
	if hits < hitFloor {
		t.Errorf("hit rate fell to %d/%d, floor is %d", hits, len(corpus), hitFloor)
	}
}

// The gate must stay shut on questions the network has nothing for. If
// these start "hitting", the relevance gate has been loosened too far and
// the hit rate above is measuring noise.
func TestQuestionsWithNoAnswerStayMisses(t *testing.T) {
	e, _ := seedCorpus(t)
	for _, q := range []string{
		"how to bake a chocolate cake",
		"what is the capital of France",
		"set up kubernetes ingress with cert-manager",
		"write a haiku about garbage collection",
	} {
		resp := e.ask(corpusEntry{query: q, from: goEnv(), tree: []string{
			"pkg:golang/github.com/google/uuid@v1.6.0",
			"pkg:golang/gorm.io/gorm@v1.31.2",
		}})
		if !resp.Miss || len(resp.Results) > 0 {
			t.Errorf("%q was answered with %s: a wrong hit is worse than a miss",
				q, resp.Results[0].SampleID)
		}
	}
}

// The caller's dependency tree must rank, never decide. The same question
// asked from an empty directory and from a project full of unrelated
// packages must reach the same sample — that difference is exactly the
// defect that made four correct answers disappear.
func TestTheCallersProjectDoesNotChangeTheAnswer(t *testing.T) {
	e, corpus := seedCorpus(t)
	for _, c := range corpus {
		bare := e.ask(corpusEntry{query: c.query})
		full := e.ask(c)
		got := func(r domain.SearchResponse) string {
			if r.Miss || len(r.Results) == 0 {
				return "MISS"
			}
			return r.Results[0].SampleID
		}
		if got(bare) != got(full) {
			t.Errorf("%q: empty directory answered %s, inside a project answered %s",
				c.query, got(bare), got(full))
		}
	}
}
