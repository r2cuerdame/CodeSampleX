package search

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

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
		saveResolvedReceipt(t, db, s.id, m, "ed25519:aaaaaaaaaaaaaaaa")
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
		// corpusEntry.from models the automatically scanned current project,
		// not an environment the caller explicitly constrained.
		EnvironmentProvenance: domain.SearchProvenanceContext,
		Limit:                 3,
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

// EXACT is a claim about the ENVIRONMENT as much as the version: nothing
// here differs from yours. A caller that supplies no environment — every
// MCP client that omits the field — compared nothing, every dimension was
// skipped for want of a value on one side, and the empty difference list
// read as agreement. The most confident grade the system has was handed
// out on no evidence at all.
func TestAnUnknownEnvironmentIsNeverGradedExact(t *testing.T) {
	e, _ := seedCorpus(t)

	blind := e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "verify a JWT with golang-jwt v5",
		Packages:      []string{"pkg:golang/github.com/golang-jwt/jwt/v5@v5.3.0"},
	})
	if blind.Miss || len(blind.Results) == 0 {
		t.Fatal("an exact package match with no environment should still answer")
	}
	if g := blind.Results[0].Grade; g == domain.GradeExact {
		t.Errorf("grade = %s with no environment supplied: silence is not agreement", g)
	}

	// Supplying the environment the sample was verified on still earns it.
	seen := e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "verify a JWT with golang-jwt v5",
		Packages:      []string{"pkg:golang/github.com/golang-jwt/jwt/v5@v5.3.0"},
		Environment:   alpineEnv("golang", "go", "1.26.0", "go"),
	})
	if seen.Miss || len(seen.Results) == 0 {
		t.Fatal("the matching environment should answer")
	}
	if g := seen.Results[0].Grade; g != domain.GradeExact {
		t.Errorf("grade = %s on the sample's own environment, want EXACT", g)
	}
}

// bm25 was computed, ranked, and thrown away. Shard sync indexes a sample
// as "sample:"+sampleID and SeedSampleDoc — the path every other test uses
// — indexes the bare id, so on a real install, where every candidate comes
// from a shard, no FTS hit ever matched a candidate.
//
// weightFTS is the largest single relevance term. Without it the score was
// intentOverlap alone: shared tokens divided by the LENGTH of the question,
// so asking something longer and more specific scored lower. On the live
// network "clap derive parse" answered and "parse command line flags in
// rust with clap" returned NO_SAFE_MATCH, for the same sample.
func TestLexicalRelevanceSurvivesTheShardDocIdPrefix(t *testing.T) {
	if got := sampleIDFromDocID("sample:sha256:abc"); got != "sha256:abc" {
		t.Fatalf("sampleIDFromDocID = %q", got)
	}
	if got := sampleIDFromDocID("sha256:abc"); got != "sha256:abc" {
		t.Fatalf("bare id changed: %q", got)
	}

	e, _ := seedCorpus(t)
	long := e.ask(corpusEntry{
		query: "test axum handlers and routing without binding a port in rust",
		from:  goEnv(), tree: []string{"pkg:golang/gorm.io/gorm@v1.31.2"},
	})
	short := e.ask(corpusEntry{query: "axum oneshot", from: goEnv()})
	for name, r := range map[string]domain.SearchResponse{"long": long, "short": short} {
		if r.Miss || len(r.Results) == 0 {
			t.Fatalf("%s question missed", name)
		}
		if r.Results[0].SampleID != "sha256:axum1" {
			t.Errorf("%s question answered %s", name, r.Results[0].SampleID)
		}
	}
	// The longer, more specific question must not score WORSE than the
	// two-word one — that inversion is the symptom the defect produced.
	if long.Results[0].Score < short.Results[0].Score*0.5 {
		t.Errorf("specific question scored %.3f against %.3f for two words",
			long.Results[0].Score, short.Results[0].Score)
	}
}

// Naming the library is the strongest thing a question can do short of
// pinning a version, and it only opened the relevance gate. "parse command
// line flags in rust with clap" ranked an npm commander sample above the
// clap one: BM25 loves "parse command line", and nothing carried the two
// words that settle it.
func TestNamingThePackageOutranksLexicalOverlap(t *testing.T) {
	e, _ := seedCorpus(t)
	r := e.ask(corpusEntry{
		query: "render a component to an html string with react-dom",
		from:  goEnv(), tree: []string{"pkg:golang/github.com/google/uuid@v1.6.0"},
	})
	if r.Miss || len(r.Results) == 0 {
		t.Fatal("naming the package should answer")
	}
	if r.Results[0].SampleID != "sha256:react1" {
		t.Errorf("answered %s; the question names react-dom", r.Results[0].SampleID)
	}
}

// An unnamed receipt is not a peer. PeerID "" counted as a distinct key, so
// one anonymous receipt beside one real one reached L4 — "contract-PASS
// from two INDEPENDENT peers" — and took the ×3 multiplier with it.
// Independence is the one thing a verification level asserts that a single
// publisher cannot manufacture.
func TestAnonymousReceiptsDoNotManufactureIndependence(t *testing.T) {
	pass := map[string]string{"contract": "PASS"}
	mixed := []domain.VerificationReceipt{
		{PeerID: "", Stages: pass},
		{PeerID: "ed25519:aaaa", Stages: pass},
	}
	if lvl := verificationLevel("", nil, mixed); lvl >= 4 {
		t.Errorf("level %d from one named peer and one anonymous receipt", lvl)
	}
	two := []domain.VerificationReceipt{
		{PeerID: "ed25519:aaaa", Stages: pass},
		{PeerID: "ed25519:bbbb", Stages: pass},
	}
	if lvl := verificationLevel("", nil, two); lvl < 4 {
		t.Errorf("level %d from two genuinely distinct peers, want >= 4", lvl)
	}
}

// A result demoted to REFERENCE_ONLY because it is about a different
// package said nothing about why: relNone had no case in the delta, so the
// reader saw the demotion with no reason attached.
func TestADifferentPackageIsStatedInTheDelta(t *testing.T) {
	e, _ := seedCorpus(t)
	r := e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "render a react component to an html string",
		Packages:      []string{"pkg:npm/react@19.2.8"}, // sample is react-dom
		Environment:   goEnv(),
	})
	if r.Miss || len(r.Results) == 0 {
		t.Skip("no result to inspect")
	}
	res := r.Results[0]
	if res.Grade == domain.GradeReferenceOnly && len(res.Different) == 0 {
		t.Error("REFERENCE_ONLY with an empty Different list: the reason is invisible")
	}
}

// Architecture and libc were not compared at all, so a caller on glibc/x64
// was told a sample verified on musl/arm64 was an EXACT match with an empty
// difference list. Those are the two dimensions that most often decide
// whether a package with a native module loads at all — the whole reason
// the fingerprint carries them.
func TestArchAndLibcAreCompared(t *testing.T) {
	sam := alpineEnv("npm", "node", "22.18.1", "javascript") // musl, x64
	sam.Arch = "arm64"

	caller := alpineEnv("npm", "node", "22.18.1", "javascript")
	caller.Arch, caller.Libc = "x64", "glibc"
	caller.OSVersionBucket, caller.Virtualization, caller.ContainerRuntime = "debian", "", ""

	dims := compareEnv(caller.Normalize(), sam.Normalize(), "npm", false)
	grade, _ := buildGrade(relExactVersion, dims, contextDelta{}, false)
	if grade == domain.GradeExact {
		t.Error("EXACT for a glibc/x64 caller against a musl/arm64 sample")
	}
	_, different := buildDelta(relExactVersion,
		domain.PURL{Ecosystem: "npm", Name: "esbuild", Version: "0.25.0"},
		domain.PURL{Ecosystem: "npm", Name: "esbuild", Version: "0.25.0"},
		dims, contextDelta{})
	var sawArch, sawLibc bool
	for _, d := range different {
		if strings.Contains(d, "arm64") || strings.Contains(d, "x64") {
			sawArch = true
		}
		if strings.Contains(d, "musl") || strings.Contains(d, "glibc") {
			sawLibc = true
		}
	}
	if !sawArch {
		t.Errorf("the architecture difference is not stated: %v", different)
	}
	if !sawLibc {
		t.Errorf("the libc difference is not stated: %v", different)
	}
}

// A browser-major difference CAPS the grade at ADAPTATION_REQUIRED and puts
// "verify in safari 19" in the adaptation list, and only a context MISMATCH
// was rendered into the delta — so the answer came back telling the reader
// to adapt without telling them to what. The sample's own browser appeared
// nowhere.
func TestAnAdaptableContextDifferenceIsStated(t *testing.T) {
	req := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "browser",
		ExecutionContext: "browser", BrowserFamily: "safari", BrowserMajor: "19",
	}.Normalize()
	sam := req
	sam.BrowserMajor = "15"

	cd := compareContext(req, sam.Normalize())
	if !cd.browserAdapt {
		t.Fatalf("fixture does not produce a browser adaptation: %+v", cd)
	}
	grade, adapt := buildGrade(relExactVersion, nil, cd, false)
	if grade != domain.GradeAdaptationRequired || len(adapt) == 0 {
		t.Fatalf("grade=%s adapt=%v", grade, adapt)
	}
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	_, different := buildDelta(relExactVersion, p, p, nil, cd)
	if len(different) == 0 {
		t.Fatal("told to adapt, with nothing said about what differs")
	}
	var saidSo bool
	for _, d := range different {
		if strings.Contains(d, "15") {
			saidSo = true
		}
	}
	if !saidSo {
		t.Errorf("the sample's own browser is never shown: %v", different)
	}
}

// A shard old enough to carry no packages field is not authoritative about
// any version. The cap only fired for relations ABOVE relPackageOnly, so a
// relMajorDiff computed from the shard KEY survived and the result asserted
// a version difference the sample had never declared — and forced
// REFERENCE_ONLY on that invented basis.
func TestAnUnestablishedVersionCannotAssertADifferenceEither(t *testing.T) {
	c := &candidate{
		sampleID: "sha256:shardonly",
		// declared is empty: the shard predates the packages field.
		packages: []domain.PURL{{Ecosystem: "npm", Name: "axios", Version: "7.0.0"}},
		caseObj:  &domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "post json with axios"},
	}
	e, _ := seedCorpus(t)
	res, _ := e.scoreCandidate(e.ctx, domain.SearchRequest{
		SchemaVersion: 1, Query: "post json with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"},
	}, domain.EnvironmentFingerprint{SchemaVersion: 1},
		[]domain.PURL{{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}},
		c, map[string]*pkgEvidence{}, time.Now().UTC())

	for _, d := range res.Different {
		if strings.Contains(d, "7.0") {
			t.Errorf("asserted a version the sample never declared: %q", d)
		}
	}
}

// A version nobody stated is not a version that differs.
//
// The runtime branch checked that both sides name a RUNTIME and then
// compared their VERSIONS, so a sample declaring runtime "node" with no
// version read as a different major from any versioned request — forced to
// REFERENCE_ONLY, with the delta printing "Sample uses node, current
// project uses node 22" as though that were a difference.
//
// Found by asking the live network a real question through MCP: the sample
// that answered it exactly came back REFERENCE_ONLY at 0.35 while an
// unrelated sample for the same package took the top slot at EXACT 0.96,
// purely because the unrelated one happened to state a version.
func TestAnUnstatedVersionIsNotADifferentVersion(t *testing.T) {
	req := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "node", RuntimeVersion: "22.18",
		Language: "javascript", LanguageVersion: "es2024", ModuleSystem: "esm",
	}.Normalize()

	// The sample names its runtime and language but no versions.
	sam := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", Runtime: "node",
		Language: "javascript", ModuleSystem: "esm",
	}.Normalize()

	dims := compareEnv(req, sam, "npm", false)
	for _, d := range dims {
		if d.refOnly {
			t.Errorf("an unstated version forced REFERENCE_ONLY: sample %q vs request %q",
				d.samShow, d.reqShow)
		}
	}
	if g, _ := buildGrade(relExactVersion, dims, compareContext(req, sam), false); g == domain.GradeReferenceOnly {
		t.Error("grade REFERENCE_ONLY on versions neither side compared")
	}

	// A version both sides DO state, and which really differs, still demotes.
	known := sam
	known.RuntimeVersion = "18.20"
	var sawRefOnly bool
	for _, d := range compareEnv(req, known.Normalize(), "npm", false) {
		if d.refOnly {
			sawRefOnly = true
		}
	}
	if !sawRefOnly {
		t.Error("node 18 against node 22 should still be REFERENCE_ONLY")
	}
}

// A caller who NAMED packages and got a sample about none of them has not
// been answered. relNone was graded REFERENCE_ONLY and RETURNED, so "parse
// a large CSV lazily without loading it into memory" with pkg:pypi/polars
// named came back with an Elixir nimble_csv sample — a different package
// in a different language, honestly labelled and still not an answer.
//
// The cost is not only the noise. A returned result is not a miss, so the
// question was never recorded as wanted and nobody learned that a polars
// sample was needed: the wrong answer displaced the demand signal that
// would have fixed it.
func TestADifferentPackageIsAMissNotAReference(t *testing.T) {
	e, _ := seedCorpus(t)
	r := e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "render a react component to an html string",
		Packages:      []string{"pkg:npm/preact@10.28.1"}, // no sample names preact
		Environment:   goEnv(),
	})
	if !r.Miss || len(r.Results) > 0 {
		t.Errorf("answered a preact question with %d result(s): %v",
			len(r.Results), r.Results[0].Case.Goal)
	}

	// Naming a package the network DOES have still answers.
	ok := e.Search(e.ctx, domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "render a react component to an html string",
		Packages:      []string{"pkg:npm/react-dom@19.2.8"},
		Environment:   goEnv(),
	})
	if ok.Miss || len(ok.Results) == 0 {
		t.Fatal("naming a package the network has should still answer")
	}
	if ok.Results[0].SampleID != "sha256:react1" {
		t.Errorf("answered with %s", ok.Results[0].SampleID)
	}
}

// A question that names no language is genuinely ambiguous — "run at most
// N async operations at once" is answered by p-limit on npm and by
// package:pool on Dart, and neither is wrong. What must NOT be ambiguous is
// what happens once the caller names a package: that settles the ecosystem
// completely, and the other one must not win on lexical similarity.
//
// Measured on the live network, where both samples exist and the bare
// question returned the Dart one.
func TestNamingThePackageSettlesTheEcosystem(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	for _, s := range []struct {
		id, goal, purl string
		env            domain.EnvironmentFingerprint
	}{
		{"sha256:plimit1", "Cap how many async tasks run at once with p-limit",
			"pkg:npm/p-limit@7.1.1", alpineEnv("npm", "node", "22.18.1", "javascript")},
		{"sha256:pool1", "Run at most N async operations at once in Dart with package:pool",
			"pkg:pub/pool@1.5.1", alpineEnv("pub", "dart", "3.9.0", "dart")},
	} {
		m := mkManifest(s.goal, []string{s.purl}, s.env)
		if err := SeedSampleDoc(ctx, db, m, s.id, "PUBLISHED"); err != nil {
			t.Fatal(err)
		}
	}
	e := &testEngine{Engine{DB: db}, ctx}

	for _, tc := range []struct{ pkg, want string }{
		{"pkg:npm/p-limit@7.1.1", "sha256:plimit1"},
		{"pkg:pub/pool@1.5.1", "sha256:pool1"},
	} {
		r := e.Search(ctx, domain.SearchRequest{
			SchemaVersion: 1,
			Query:         "run at most N async operations at once",
			Packages:      []string{tc.pkg},
		})
		if r.Miss || len(r.Results) == 0 {
			t.Errorf("%s: missed", tc.pkg)
			continue
		}
		if r.Results[0].SampleID != tc.want {
			t.Errorf("%s answered with %s, want %s", tc.pkg, r.Results[0].SampleID, tc.want)
		}
	}
}

// os, arch and libc were appended ONLY when they disagreed, so a caller
// whose machine matched the sample saw none of them in the Exact list —
// the answer named the package and the runtime and stayed silent about the
// machine, which is the part a reader came for. Worse, a request that
// knows only those three produced no comparable dimension at all, so a
// perfect match was capped at COMPATIBLE for want of anything to compare.
func TestAMatchingMachineIsStatedAndCounts(t *testing.T) {
	sam := alpineEnv("npm", "node", "22.18.1", "javascript") // linux/musl/x64
	dims := compareEnv(sam, sam, "npm", false)

	var sawOS, sawArch, sawLibc bool
	for _, d := range dims {
		if !d.equal {
			continue
		}
		switch {
		case strings.Contains(d.exactEntry, "linux"):
			sawOS = true
		case d.exactEntry == "x64":
			sawArch = true
		case d.exactEntry == "musl":
			sawLibc = true
		}
	}
	if !sawOS || !sawArch || !sawLibc {
		t.Errorf("a matching machine is not stated: os=%v arch=%v libc=%v", sawOS, sawArch, sawLibc)
	}

	// A request that knows ONLY the machine still has something to compare,
	// so a real match can reach EXACT.
	machineOnly := domain.EnvironmentFingerprint{
		SchemaVersion: 1, OS: "linux", Arch: "x64", Libc: "musl",
	}.Normalize()
	got := compareEnv(machineOnly, sam, "npm", false)
	if len(got) == 0 {
		t.Fatal("a machine-only request compared nothing")
	}
	if g, _ := buildGrade(relExactVersion, got, contextDelta{}, false); g != domain.GradeExact {
		t.Errorf("grade = %s on a machine that matches exactly", g)
	}
}

// The NETWORK decides a sample's status; a local row is a cache of the
// artifact, not of the network's judgement. storeFetched writes
// "PUBLISHED" for anything it downloads, so fetching a STABLE sample
// downgraded it locally from verification level 5 to 3 — a x3 strength
// multiplier to x1 — and USING a sample made it markedly harder to find
// again, sometimes under the miss threshold entirely.
func TestFetchingASampleDoesNotDowngradeIt(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	id := "sha256:stable1"
	env := alpineEnv("npm", "node", "22.18.1", "javascript")
	m := mkManifest("Serve a JSON POST route with express and its error shape",
		[]string{"pkg:npm/express@5.1.0"}, env)

	// The shard says the network considers it STABLE.
	saveShardJSON(t, db, "npm/express/5", shardFile{
		SchemaVersion: 1, Key: "npm/express/5",
		Packages: []shardPackage{{
			PURL: "pkg:npm/express@5.1.0",
			Samples: []shardSampleEntry{{
				SampleID: id, Goal: m.Case.Goal, Status: "STABLE",
				Packages: m.Packages, Environment: env,
			}},
		}},
	})
	// get_sample then writes the local row as PUBLISHED.
	if err := SeedSampleDoc(ctx, db, m, id, "PUBLISHED"); err != nil {
		t.Fatal(err)
	}

	e := Engine{DB: db}
	cands, _, err := e.collect(ctx, domain.SearchRequest{SchemaVersion: 1, Query: m.Case.Goal})
	if err != nil {
		t.Fatal(err)
	}
	c := cands[id]
	if c == nil {
		t.Fatal("candidate missing")
	}
	if c.status != "STABLE" {
		t.Errorf("status = %q after fetching; the network said STABLE", c.status)
	}
}

// packageRelation kept the FRIENDLIEST shared pair, so a caller naming
// [react@19.2.0, react-dom@19.2.0] against a sample declaring
// [react@19.2.0, react-dom@18.3.1] was told MATCH: EXACT on react while the
// react-dom major gap — the thing that would actually break their build —
// went unmentioned in the delta. The server-side search already grades the
// widest gap for exactly this reason, so the same input got two answers
// depending on which path served it.
func TestTheWidestSharedGapIsTheOneGraded(t *testing.T) {
	req := parsePURLs([]string{"pkg:npm/react@19.2.0", "pkg:npm/react-dom@19.2.0"})
	sam := parsePURLs([]string{"pkg:npm/react@19.2.0", "pkg:npm/react-dom@18.3.1"})

	rel, _, samP := packageRelation(req, sam)
	if rel == relExactVersion {
		t.Error("graded EXACT while a shared package differs by a major version")
	}
	if samP.Name != "react-dom" {
		t.Errorf("reported %s; the widest gap is react-dom", samP.Name)
	}
	// Order must not decide it.
	rev := parsePURLs([]string{"pkg:npm/react-dom@19.2.0", "pkg:npm/react@19.2.0"})
	if r2, _, _ := packageRelation(rev, sam); r2 != rel {
		t.Errorf("the answer depends on array order: %v vs %v", rel, r2)
	}
	// Everything agreeing is still EXACT.
	same := parsePURLs([]string{"pkg:npm/react@19.2.0", "pkg:npm/react-dom@19.2.0"})
	if r3, _, _ := packageRelation(req, same); r3 != relExactVersion {
		t.Errorf("relation = %v when every shared package matches exactly", r3)
	}
	// A package the sample does not have at all is still relNone.
	if r4, _, _ := packageRelation(parsePURLs([]string{"pkg:npm/preact@10.28.1"}), sam); r4 != relNone {
		t.Errorf("relation = %v for a package the sample never names", r4)
	}
}
