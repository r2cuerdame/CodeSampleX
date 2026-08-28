package web

import (
	"net/http"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// R2C-127, reported from production on 2026-08-27, at one exact URL:
//
//	/golang/github.com/jackc/pgx/v5?f_os=ubuntu+glibc&f_symbol=Batch&f_version=v5.10.0&lang=ko#cube
//
// Three things were wrong there, and they are one thing:
//
//  1. the page said a sample existed more than once — in the answer card, in
//     the exact record under it, and in the cell above both;
//  2. the copy in the exact record LOOKED actionable and was not clickable;
//  3. what a reader standing on a coordinate with a sample actually needs is
//     not a third sentence saying there is one, it is the way to open it.
//
// A repeated status line and a dead affordance are the same defect seen twice:
// the page spending the reader's attention on a fact it has already given them
// instead of on the thing they came for. So the rules these tests hold are:
//
//	one screen, one statement that a sample is here
//	a sample that exists is REACHABLE from the coordinate that says so
//	no sample, no action — an empty destination is worse than no link
//
// The fixture is the production coordinate: pgx/v5 v5.10.0, the Batch API, on
// ubuntu glibc, with one published answer for that release and API.

const (
	pgxEco     = "golang"
	pgxName    = "github.com/jackc/pgx/v5"
	pgxVersion = "v5.10.0"
	pgxSymbol  = "Batch"
	pgxPurl    = "pkg:" + pgxEco + "/" + pgxName + "@" + pgxVersion
	pgxSample  = "sha256:pgxbatch"
	pgxGoal    = "send a batch of queries on one round trip"
)

// pgxLeaf is the reported URL, verbatim apart from the fragment the server
// never sees.
const pgxLeaf = "/golang/github.com/jackc/pgx/v5" +
	"?f_os=ubuntu+glibc&f_symbol=Batch&f_version=v5.10.0&lang=ko"

// pgxBatchSnap is the coordinate as production recorded it: reported builds
// from real projects on ubuntu glibc, plus contracts of ours that ran there.
//
// The OS dimension is assembled from the fingerprint, not stored as a string:
// distro replaces the kernel name and libc is appended, which is what makes
// "ubuntu glibc" the value the reported link pins (osLabel).
const pgxBatchSnap = `{
  "schemaVersion": 1,
  "purl": "` + pgxPurl + `",
  "symbol": "` + pgxSymbol + `",
  "generatedAt": "2026-08-27T00:00:00Z",
  "rows": [{
    "envBucket": {"schemaVersion":1,"os":"linux","distro":"ubuntu","libc":"glibc",
      "arch":"x64","runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
    "confidence": "MEDIUM",
    "passRate": 0.8,
    "uniquePeerBuckets": 11,
    "lastSeen": "2026-08-26T10:00:00Z",
    "byStage": {"PROJECT_COMPILE": {"pass": 551, "fail": 138},
                "CONTRACT": {"pass": 3, "fail": 0}}
  }],
  "failures": []
}`

// pgxObservedOnlySnap is the same coordinate with nothing of ours run there:
// real projects reported builds and this network published no answer.
//
// A recorded contract of OURS is itself proof a sample was here — that rule is
// in deriveSampleState and it is the right one — so "no sample" cannot be
// modelled by deleting the sample list alone. The honest absence is an
// observation-only coordinate, which is also the shape most of production is.
const pgxObservedOnlySnap = `{
  "schemaVersion": 1,
  "purl": "` + pgxPurl + `",
  "symbol": "` + pgxSymbol + `",
  "generatedAt": "2026-08-27T00:00:00Z",
  "rows": [{
    "envBucket": {"schemaVersion":1,"os":"linux","distro":"ubuntu","libc":"glibc",
      "arch":"x64","runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
    "confidence": "MEDIUM",
    "passRate": 0.8,
    "uniquePeerBuckets": 11,
    "lastSeen": "2026-08-26T10:00:00Z",
    "byStage": {"PROJECT_COMPILE": {"pass": 551, "fail": 138}}
  }],
  "failures": []
}`

func pgxStore() *fakeStore {
	f := newFakeStore()
	f.versions[pgxEco+"|"+pgxName] = []string{pgxVersion}
	f.symbols[pgxEco+"|"+pgxName+"|"+pgxVersion] = []string{pgxSymbol}
	f.snapshots[snapKey(pgxPurl, pgxSymbol)] = pgxBatchSnap
	f.sampleList = []SampleListItem{{
		SampleID: pgxSample, Goal: pgxGoal,
		Status: "PUBLISHED", License: "MIT-0", Context: "go 1.26",
		CreatedAt: "2026-08-20", Version: pgxVersion,
		Symbols: []string{"github.com/jackc/pgx/v5.Batch"}, Kind: "HOW",
	}}
	f.samplePackages = map[string][]string{pgxSample: {pgxPurl}}
	f.samples[pgxSample] = SampleMeta{
		SampleID: pgxSample, Status: "PUBLISHED", License: "MIT-0",
		CreatedAt: "2026-08-20T00:00:00Z", ManifestJSON: pgxManifest,
		Files: []string{"csx.json", "main_test.go"},
	}
	return f
}

const pgxManifest = `{
  "schemaVersion": 1,
  "case": {
    "schemaVersion": 1,
    "caseId": "case:sha256:b47c",
    "kind": "HOW",
    "goal": "` + pgxGoal + `",
    "packages": ["` + pgxPurl + `"],
    "contract": ["every queued query reports its own result"]
  },
  "packages": ["` + pgxPurl + `"],
  "symbols": ["github.com/jackc/pgx/v5.Batch"],
  "environment": {"schemaVersion":1,"ecosystem":"golang","os":"linux","distro":"ubuntu",
    "libc":"glibc","arch":"amd64","runtime":"go","runtimeVersion":"1.26"},
  "license": "MIT-0",
  "contractCommand": ["go", "test", "./..."],
  "verifierAdapter": "go@1"
}`

// sampleStatements counts the places a page states, in its own words, that a
// sample is here.
//
// One per BLOCK, not one per occurrence: a single chip writes its sentence
// three times — as the icon's accessible name, as its tooltip and as the text
// beside it — and those are one statement to a reader, deliberately kept
// identical so the eye, the mouse and a screen reader are told the same thing.
// What must not repeat is the block.
func sampleStatements(body string) int {
	n := 0
	for _, block := range []string{
		`class="state sample`,    // the answer card's chip
		`class="leafcode`,        // an exact record's own line
		`class="cube-code-state`, // the existence line above an environment grid
	} {
		n += strings.Count(body, block)
	}
	return n
}

// 11. The reported coordinate states it once.
func TestTheExactPgxLeafStatesTheSampleOnce(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = pgxStore() })
	body := get(t, mux, pgxLeaf).Body.String()
	section := cubeSection(t, body)

	card := answerCard(t, body)
	if !strings.Contains(card, `class="smark s-pass"`) {
		t.Fatalf("the coordinate ran clean and the card does not say so:\n%s", card)
	}
	if n := sampleStatements(section); n != 1 {
		t.Errorf("the sample is stated %d times on one screen, want once:\n%s", n, section)
	}
	// And the record that used to repeat it is gone: one exact record is
	// fully stated by the card above it, so printing it again is the third
	// reading of the same coordinate the report was about.
	if strings.Contains(section, `class="cubeleaf`) {
		t.Errorf("a single exact record is listed under the card that states it:\n%s", section)
	}
}

// 12. Where a sample exists, the coordinate that says so can reach it.
func TestTheExactPgxLeafSampleActionReachesTheSample(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = pgxStore() })
	card := answerCard(t, get(t, mux, pgxLeaf).Body.String())

	href := hrefOfClass(t, card, "evaction ev-samples")
	if href == "" {
		t.Fatalf("a coordinate with a published sample offers no way to it:\n%s", card)
	}
	// Not a claim about the link's shape — a claim about what is behind it.
	// The action is worth the reader's click only if the page it opens holds
	// the sample, and only an actual request can say whether it does.
	listing := mustGet(t, mux, href)
	if !strings.Contains(listing, sampleHref(pgxSample)) {
		t.Fatalf("%s does not reach the sample it promised:\n%s", href, truncate(listing))
	}
	detail := mustGet(t, mux, sampleHref(pgxSample))
	if !strings.Contains(detail, pgxGoal) {
		t.Errorf("the canonical sample page is not the sample:\n%s", truncate(detail))
	}
}

// Every affordance the card offers goes somewhere. A link that 404s or lands
// on an empty page is the dead end, whether or not it was labelled "code".
func TestNoActionOnTheExactPgxLeafIsDead(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = pgxStore() })
	card := answerCard(t, get(t, mux, pgxLeaf).Body.String())

	hrefs := regexpAllHrefs(card)
	if len(hrefs) == 0 {
		t.Fatalf("the card offers nothing at all at a coordinate with evidence:\n%s", card)
	}
	for _, href := range hrefs {
		if strings.HasPrefix(href, "#") {
			continue
		}
		if rec := get(t, mux, href); rec.Code != http.StatusOK {
			t.Errorf("%s answers %d — the card offers a door onto nothing", href, rec.Code)
		}
	}
}

// 13. No sample, no action, and nothing shaped like one. The absence is
// STATED — "there is nothing here" is an answer — and it is not a link.
func TestTheExactPgxLeafOffersNothingWhenThereIsNoSample(t *testing.T) {
	bare := pgxStore()
	bare.sampleList, bare.samplePackages = nil, map[string][]string{}
	bare.samples = map[string]SampleMeta{}
	bare.snapshots[snapKey(pgxPurl, pgxSymbol)] = pgxObservedOnlySnap
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = bare })
	body := get(t, mux, pgxLeaf).Body.String()
	section := cubeSection(t, body)
	card := answerCard(t, body)

	if strings.Contains(section, "ev-samples") {
		t.Errorf("a coordinate with no published sample still offers code:\n%s", section)
	}
	if want := sampleStateLabel("ko", sampleNone); !strings.Contains(card, want) {
		t.Errorf("the absence is not stated, it is merely missing (%q):\n%s", want, card)
	}
	// Our own contracts still ran here, and the measured record for the
	// coordinate is still worth opening — so the card keeps that one action
	// and drops only the one whose destination is empty.
	if !strings.Contains(card, "ev-records") {
		t.Errorf("the measured record went away with the sample:\n%s", card)
	}
	// And no document: a coordinate with nothing published draws no mark at
	// all, because a mark is the affordance onto code that is not there.
	if strings.Contains(section, `class="smark s-`) {
		t.Errorf("a coordinate with no sample still draws a document:\n%s", section)
	}
	// The stated absence must not be dressed as something to click.
	for _, href := range regexpAllHrefs(card) {
		if strings.Contains(href, "/samples/") {
			t.Errorf("a coordinate with no sample links to one: %s", href)
		}
	}
}

// The coordinate itself is stated once too. The reported URL pins the OS as
// "ubuntu glibc", which is one dimension carrying two facts on purpose — and
// the libc dimension printed the second of them again, so the line read
// "ubuntu glibc · glibc · x64".
func TestTheExactPgxLeafNamesItsEnvironmentOnce(t *testing.T) {
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = pgxStore() })
	card := answerCard(t, get(t, mux, pgxLeaf).Body.String())

	i := strings.Index(card, `class="answer-env dim"`)
	if i < 0 {
		t.Fatalf("the card never names the environment it is about:\n%s", card)
	}
	env := card[i:]
	env = env[strings.Index(env, ">")+1 : strings.Index(env, "</span>")]
	if !strings.Contains(env, "ubuntu glibc") {
		t.Fatalf("the card does not name the pinned platform: %q", env)
	}
	if n := strings.Count(env, "glibc"); n != 1 {
		t.Errorf("the environment line says glibc %d times: %q", n, env)
	}

	// The navigator names the same place one rung up, and it repeated the
	// same word for the same reason.
	body := get(t, mux, pgxLeaf).Body.String()
	nav := body[strings.Index(body, `class="cubenav`):]
	nav = nav[:strings.Index(nav, "</nav>")]
	rung := ""
	for _, step := range strings.Split(nav, `class="navvalue"`)[1:] {
		step = step[strings.Index(step, ">")+1 : strings.Index(step, "</span>")]
		if strings.Contains(step, "ubuntu") {
			rung = step
		}
	}
	if rung == "" {
		t.Fatalf("the navigator does not name the pinned platform:\n%s", nav)
	}
	if n := strings.Count(rung, "glibc"); n != 1 {
		t.Errorf("the navigator's environment rung says glibc %d times: %q", n, rung)
	}
}

// Several exact records under one card: the sample sentence belongs to the
// release and the API, so the card says it and the records say what differs.
func TestExactRecordsDoNotRepeatTheCardsSampleSentence(t *testing.T) {
	f := pgxStore()
	// A second recorded environment for the same release and API, so the
	// slice is no longer decided and the records render as a list.
	f.snapshots[snapKey(pgxPurl, pgxSymbol)] = twoBucketPgxSnap
	mux, _ := newTestMux(t, func(d *Deps) { d.Store = f })
	body := get(t, mux, "/golang/github.com/jackc/pgx/v5"+
		"?f_symbol=Batch&f_version=v5.10.0&lang=ko").Body.String()
	section := cubeSection(t, body)
	if !strings.Contains(section, `class="cubeleaf`) {
		t.Fatalf("the fixture did not produce a list of exact records:\n%s", truncate(section))
	}

	card := answerCard(t, body)
	// Pass and fail are both on record here, which is the shape production
	// actually carries at this coordinate, so the card's own state is mixed.
	if !strings.Contains(card, sampleStateLabel("ko", sampleMixed)) {
		t.Fatalf("the card does not state the sample:\n%s", card)
	}
	// The records still carry a MARK each — its colour is that record's own
	// outcome — and they no longer carry the card's sentence as text.
	leaf := section[strings.Index(section, `class="cubeleaf`):]
	if !strings.Contains(leaf, `class="smark s-`) {
		t.Errorf("the records lost the mark along with the words:\n%s", leaf)
	}
	// Every state's sentence opens with the same clause, so what must not
	// appear as text under the card is any of them.
	for _, line := range strings.Split(leaf, `class="leafcode`)[1:] {
		line = line[:strings.Index(line, "</p>")]
		visible := line[strings.Index(line, "</svg></span>")+len("</svg></span>"):]
		for _, state := range sampleStates {
			if state == sampleNone {
				continue
			}
			if strings.Contains(visible, sampleStateLabel("ko", state)) {
				t.Errorf("an exact record restates the sample as text:\n%s", line)
			}
		}
	}
	// What each record does still say for itself: its own outcome, in words
	// on the line above the mark, and in the mark's colour. One of these
	// records failed where the others passed — the difference the reader came
	// down here for — and it is legible without the repeated clause.
	if !strings.Contains(leaf, `class="smark s-fail"`) {
		t.Errorf("the failing record lost the mark that says so:\n%s", leaf)
	}
	if !strings.Contains(leaf, "2회 중 0회 통과") {
		t.Errorf("the failing record does not state its own outcome in words:\n%s", leaf)
	}
}

// twoBucketPgxSnap records the same release and API in two environments, one
// of which our fleet ran and one of which only projects reported.
const twoBucketPgxSnap = `{
  "schemaVersion": 1,
  "purl": "` + pgxPurl + `",
  "symbol": "` + pgxSymbol + `",
  "generatedAt": "2026-08-27T00:00:00Z",
  "rows": [{
    "envBucket": {"schemaVersion":1,"os":"linux","distro":"ubuntu","libc":"glibc",
      "arch":"x64","runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
    "confidence": "MEDIUM",
    "passRate": 0.8,
    "lastSeen": "2026-08-26T10:00:00Z",
    "byStage": {"PROJECT_COMPILE": {"pass": 551, "fail": 138},
                "CONTRACT": {"pass": 3, "fail": 0}}
  },{
    "envBucket": {"schemaVersion":1,"os":"linux","distro":"alpine","libc":"musl",
      "arch":"x64","runtime":"go","runtimeVersion":"1.26","packageManager":"go"},
    "confidence": "MEDIUM",
    "passRate": 1,
    "lastSeen": "2026-08-26T10:00:00Z",
    "byStage": {"CONTRACT": {"pass": 2, "fail": 0}}
  },{
    "envBucket": {"schemaVersion":1,"os":"linux","distro":"ubuntu","libc":"glibc",
      "arch":"x64","runtime":"go","packageManager":"go"},
    "confidence": "MEDIUM",
    "passRate": 0,
    "lastSeen": "2026-08-26T10:00:00Z",
    "byStage": {"CONTRACT": {"pass": 0, "fail": 2}}
  }],
  "failures": []
}`

// hrefOfClass returns the href of the first element carrying exactly this
// class attribute, or "" when the page draws none.
func hrefOfClass(t *testing.T, fragment, class string) string {
	t.Helper()
	i := strings.Index(fragment, `class="`+class+`"`)
	if i < 0 {
		return ""
	}
	open := strings.LastIndex(fragment[:i], "<a ")
	if open < 0 {
		// Present, but not on a link: exactly the dead affordance this issue
		// was reopened for, so it is named rather than reported as absent.
		t.Fatalf("%q is rendered without a link around it:\n%s", class, fragment)
	}
	rest := fragment[open:]
	h := strings.Index(rest, `href="`)
	if h < 0 || h > strings.Index(rest, ">") {
		t.Fatalf("%q is a link with no destination:\n%s", class, fragment)
	}
	rest = rest[h+len(`href="`):]
	return unescapeHref(rest[:strings.Index(rest, `"`)])
}

// unescapeHref undoes the entity escaping html/template applies to a URL's
// query separators, so the result can be requested again.
func unescapeHref(href string) string {
	return strings.ReplaceAll(href, "&amp;", "&")
}
