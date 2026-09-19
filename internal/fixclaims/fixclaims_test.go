package fixclaims

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The five outcomes the issue asks fixtures for -- confirmed fix, false
// claim, partial OS fix, not reproduced, later regression -- plus the
// states in between, each as a candidate, its runs, and the state the
// evidence must be read as. The evaluator is a pure function of the runs;
// these fixtures are its contract.
func TestEvaluateFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/evaluate.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]struct {
		Candidate Candidate `json:"candidate"`
		Runs      []Run     `json:"runs"`
		Expect    struct {
			Status      Status      `json:"status"`
			PairOutcome PairOutcome `json:"pairOutcome"`
			BadVersion  string      `json:"badVersion"`
			GoodVersion string      `json:"goodVersion"`
			Evidence    []string    `json:"evidence"`
			RegressedAt string      `json:"regressedAt"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) < 5 {
		t.Fatalf("fixtures = %d, want the five outcomes at least", len(fixtures))
	}
	for name, f := range fixtures {
		t.Run(name, func(t *testing.T) {
			ev := Evaluate(f.Candidate, f.Runs)
			if ev.Status != f.Expect.Status || ev.PairOutcome != f.Expect.PairOutcome {
				t.Fatalf("status/pair = %s/%s, want %s/%s\n%+v", ev.Status, ev.PairOutcome, f.Expect.Status, f.Expect.PairOutcome, ev)
			}
			if ev.BadVersion != f.Expect.BadVersion || ev.GoodVersion != f.Expect.GoodVersion {
				t.Fatalf("boundary = %q..%q, want %q..%q", ev.BadVersion, ev.GoodVersion, f.Expect.BadVersion, f.Expect.GoodVersion)
			}
			if f.Expect.Evidence != nil && !reflect.DeepEqual(ev.Evidence, f.Expect.Evidence) {
				t.Fatalf("evidence = %v, want %v", ev.Evidence, f.Expect.Evidence)
			}
			if f.Expect.RegressedAt != "" {
				found := false
				for _, e := range ev.Environments {
					if e.RegressedAtVersion == f.Expect.RegressedAt {
						found = true
					}
				}
				if !found {
					t.Fatalf("no environment regressed at %s: %+v", f.Expect.RegressedAt, ev.Environments)
				}
			}
			if ev.Status.Verified() && ev.VerifiedAt.IsZero() {
				t.Fatal("a verified status must carry the time it was established")
			}
			if !ev.Status.Verified() && ev.Status != StatusRegressed && !ev.VerifiedAt.IsZero() {
				t.Fatalf("%s carries a verifiedAt", ev.Status)
			}
			// Order of arrival must not matter.
			reversed := make([]Run, len(f.Runs))
			for i, r := range f.Runs {
				reversed[len(f.Runs)-1-i] = r
			}
			if again := Evaluate(f.Candidate, reversed); again.Status != ev.Status || again.PairOutcome != ev.PairOutcome {
				t.Fatalf("reversed runs evaluate to %s/%s", again.Status, again.PairOutcome)
			}
		})
	}
}

func TestEvaluateWithNoRunsIsClaimedAndNeverVerified(t *testing.T) {
	ev := Evaluate(Candidate{ClaimedFixedVersion: "1.0.1"}, nil)
	if ev.Status != StatusClaimedFix || ev.Status.Verified() || ev.PairOutcome != PairPending {
		t.Fatalf("%+v", ev)
	}
}

func TestOnlyVerifiedAndPartialCountAsEvidence(t *testing.T) {
	for _, s := range Statuses() {
		want := s == StatusVerifiedFix || s == StatusPartialFix
		if s.Verified() != want {
			t.Fatalf("%s.Verified() = %v", s, s.Verified())
		}
	}
}

func goodCandidate() Candidate {
	return Candidate{
		SchemaVersion: 1, Ecosystem: "npm", Name: "foo",
		ClaimedBadVersion: "2.4.0", ClaimedFixedVersion: "2.4.1",
		Claim:     "Fixed crash when parse() receives an empty buffer",
		SourceURL: "https://github.com/acme/foo/releases/tag/v2.4.1", SourceType: SourceReleaseNote,
		Symbols: []string{"parse"}, Confidence: ConfidenceHigh,
	}
}

func TestValidateAcceptsAGoodCandidateAndNormalizesIt(t *testing.T) {
	c := goodCandidate()
	c.Ecosystem = " NPM "
	c.Symbols = []string{"parse", "parse", " serialize "}
	c.EnvironmentHints = []string{"Windows"}
	got, rejections := Validate(c)
	if len(rejections) != 0 {
		t.Fatalf("rejections = %v", rejections)
	}
	if got.Ecosystem != "npm" || !reflect.DeepEqual(got.Symbols, []string{"parse", "serialize"}) || got.EnvironmentHints[0] != "windows" {
		t.Fatalf("not normalized: %+v", got)
	}
	if got.DedupKey() != goodCandidate().DedupKey() {
		t.Fatal("dedup key changed with whitespace and case")
	}
}

func TestValidateRejectionsAreDeterministicAndComplete(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Candidate)
		code   string
	}{
		{"schema", func(c *Candidate) { c.SchemaVersion = 2 }, RejectSchema},
		{"ecosystem", func(c *Candidate) { c.Ecosystem = "brew" }, RejectEcosystem},
		{"name", func(c *Candidate) { c.Name = "" }, RejectName},
		{"fixed missing", func(c *Candidate) { c.ClaimedFixedVersion = "" }, RejectVersion},
		{"fixed range", func(c *Candidate) { c.ClaimedFixedVersion = "^2.4.1" }, RejectVersion},
		{"bad after fixed", func(c *Candidate) { c.ClaimedBadVersion = "2.4.2" }, RejectVersionOrder},
		{"bad equals fixed", func(c *Candidate) { c.ClaimedBadVersion = "2.4.1" }, RejectVersionOrder},
		{"claim short", func(c *Candidate) { c.Claim = "fix" }, RejectClaim},
		{"source http", func(c *Candidate) { c.SourceURL = "http://github.com/x" }, RejectSourceURL},
		{"source type", func(c *Candidate) { c.SourceType = "tweet" }, RejectSourceType},
		{"reference", func(c *Candidate) { c.References = []string{"ftp://x"} }, RejectReference},
		{"symbol", func(c *Candidate) { c.Symbols = []string{"has space"} }, RejectSymbol},
		{"hint", func(c *Candidate) { c.EnvironmentHints = []string{"Windows 11!"} }, RejectHint},
		{"confidence", func(c *Candidate) { c.Confidence = "certain" }, RejectConfidence},
		{"fingerprint", func(c *Candidate) { c.FailureFingerprintHint = "abc" }, RejectFingerprint},
		{"docs only", func(c *Candidate) { c.Claim = "Fixed a typo in the README documentation" }, RejectNonExecutable},
		{"no target", func(c *Candidate) { c.Claim = "Improved the developer experience overall"; c.Symbols = nil }, RejectNoTarget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := goodCandidate()
			tc.mutate(&c)
			_, rejections := Validate(c)
			codes := make([]string, 0, len(rejections))
			for _, r := range rejections {
				codes = append(codes, r.Code)
			}
			found := false
			for _, code := range codes {
				if code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatalf("codes = %v, want %s", codes, tc.code)
			}
			_, again := Validate(c)
			if !reflect.DeepEqual(again, rejections) {
				t.Fatal("validation is not deterministic")
			}
		})
	}
	// Several defects come back together, not one per round trip.
	c := goodCandidate()
	c.SchemaVersion = 0
	c.Ecosystem = "brew"
	c.Confidence = ""
	if _, rejections := Validate(c); len(rejections) < 3 {
		t.Fatalf("rejections = %v, want all three", rejections)
	}
}

func TestAClaimNamingASymbolNeedsNoFailureWord(t *testing.T) {
	c := goodCandidate()
	c.Claim = "Fixed behaviour of parse for empty input"
	if _, rejections := Validate(c); len(rejections) != 0 {
		t.Fatalf("%v", rejections)
	}
}

func TestCollectorExtractsExecutableFixLinesWithProvenance(t *testing.T) {
	raw, err := os.ReadFile("testdata/releases.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/foo/releases" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("token not sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()
	col := &Collector{Client: srv.Client(), BaseURL: srv.URL, Token: "tok"}
	got, err := col.Collect(t.Context(), Source{Ecosystem: "npm", Name: "foo", Repo: "acme/foo"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	// Prerelease dropped: three stable releases.
	if got.Releases != 3 {
		t.Fatalf("releases = %d, want 3", got.Releases)
	}
	byClaim := map[string]Candidate{}
	for _, c := range got.Candidates {
		byClaim[c.Claim] = c
	}
	crash, ok := byClaim["fix: crash in parse() when the input buffer is empty"]
	if !ok {
		t.Fatalf("crash line missing; got %v", keys(byClaim))
	}
	if crash.ClaimedFixedVersion != "2.4.1" || crash.ClaimedBadVersion != "2.4.0" {
		t.Fatalf("pair = %s..%s", crash.ClaimedBadVersion, crash.ClaimedFixedVersion)
	}
	if !reflect.DeepEqual(crash.Symbols, []string{"parse()"}) || crash.Confidence != ConfidenceHigh {
		t.Fatalf("symbols/confidence = %v/%s", crash.Symbols, crash.Confidence)
	}
	if !reflect.DeepEqual(crash.References, []string{"https://github.com/acme/foo/pull/512"}) {
		t.Fatalf("references = %v", crash.References)
	}
	if crash.SourceURL != "https://github.com/acme/foo/releases/tag/v2.4.1" || crash.SourceType != SourceReleaseNote {
		t.Fatalf("provenance = %s %s", crash.SourceURL, crash.SourceType)
	}
	if crash.ReleasedAt.IsZero() {
		t.Fatal("releasedAt not carried")
	}
	win, ok := byClaim["fix(windows): resolve() returned wrong drive letter on Windows"]
	if !ok || !reflect.DeepEqual(win.EnvironmentHints, []string{"windows"}) || !reflect.DeepEqual(win.References, []string{"https://github.com/acme/foo/issues/508"}) {
		t.Fatalf("windows line: %+v", win)
	}
	leak, ok := byClaim["Fixed memory leak in the connection pool"]
	if !ok || leak.ClaimedBadVersion != "" || leak.ClaimedFixedVersion != "2.3.9" || leak.Confidence != ConfidenceLow {
		t.Fatalf("oldest release: %+v", leak)
	}
	if _, ok := byClaim["docs: fix typo in README"]; ok {
		t.Fatal("a docs-only line became a candidate")
	}
	skippedDocs := false
	for _, s := range got.Skipped {
		if strings.Contains(s.Line, "typo") && s.Reason == RejectNonExecutable {
			skippedDocs = true
		}
	}
	if !skippedDocs {
		t.Fatalf("docs line not recorded as skipped: %+v", got.Skipped)
	}
	for _, c := range got.Candidates {
		if _, rejections := Validate(c); len(rejections) != 0 {
			t.Fatalf("collector produced an invalid candidate: %v", rejections)
		}
	}
}

func keys(m map[string]Candidate) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestTagVersion(t *testing.T) {
	for tag, want := range map[string]string{
		"v2.4.1": "2.4.1", "2.4.1": "2.4.1", "@scope/pkg@1.2.3": "1.2.3", "release-1.2.0": "1.2.0",
		"jackson-databind-2.17.1": "2.17.1", "v1.2.3-rc.1": "1.2.3-rc.1", "nightly": "",
	} {
		if got := tagVersion(tag, ""); got != want {
			t.Errorf("tagVersion(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestPlanAsksForThePairFirstAndExpandsOnlyOnSignal(t *testing.T) {
	c := goodCandidate()
	known := []string{"2.3.8", "2.3.9", "2.4.0", "2.4.1", "2.4.2", "2.5.0"}
	first := Plan(c, nil, known, DefaultPolicy())
	if len(first) != 2 || first[0].Version != "2.4.0" || first[1].Version != "2.4.1" || first[0].Reason != ProbeBadVersion {
		t.Fatalf("first turn = %+v", first)
	}
	// No signal: nothing more.
	notRepro := []Run{{Version: "2.4.0", Environment: DefaultEnvironment, Verdict: VerdictPass}, {Version: "2.4.1", Environment: DefaultEnvironment, Verdict: VerdictPass}}
	if probes := Plan(c, notRepro, known, DefaultPolicy()); len(probes) != 0 {
		t.Fatalf("not reproduced expanded: %+v", probes)
	}
	// Signal: one step down and one step up, never the matrix.
	fp := strings.Repeat("a", 64)
	fixed := []Run{{Version: "2.4.0", Environment: DefaultEnvironment, Verdict: VerdictFail, FailureFingerprint: fp}, {Version: "2.4.1", Environment: DefaultEnvironment, Verdict: VerdictPass}}
	probes := Plan(c, fixed, known, DefaultPolicy())
	if len(probes) != 2 || probes[0].Version != "2.3.9" || probes[0].Reason != ProbeFindFirstBad || probes[1].Version != "2.4.2" || probes[1].Reason != ProbeRegressionWatch {
		t.Fatalf("expansion = %+v", probes)
	}
	// Not fixed where it said: look one release above.
	notFixed := []Run{{Version: "2.4.0", Environment: DefaultEnvironment, Verdict: VerdictFail, FailureFingerprint: fp}, {Version: "2.4.1", Environment: DefaultEnvironment, Verdict: VerdictFail, FailureFingerprint: fp}}
	probes = Plan(c, notFixed, known, DefaultPolicy())
	if len(probes) != 1 || probes[0].Version != "2.4.2" || probes[0].Reason != ProbeLaterFix {
		t.Fatalf("later-fix = %+v", probes)
	}
	// The hard cap closes the record.
	var many []Run
	for i := 0; i < DefaultPolicy().MaxRuns; i++ {
		many = append(many, Run{Version: "2.4.0", Verdict: VerdictFail, FailureFingerprint: fp})
	}
	if probes := Plan(c, many, known, DefaultPolicy()); len(probes) != 0 {
		t.Fatalf("cap ignored: %+v", probes)
	}
}

func TestPlanHonoursEnvironmentHintsAndWorkerOS(t *testing.T) {
	c := goodCandidate()
	c.EnvironmentHints = []string{"windows"}
	probes := Plan(c, nil, nil, DefaultPolicy())
	if len(probes) != 4 || probes[2].Environment.OS != "windows" || probes[2].Reason != ProbeEnvironmentHint {
		t.Fatalf("hinted pair = %+v", probes)
	}
	linuxOnly := DefaultPolicy()
	linuxOnly.OS = []string{"linux"}
	probes = Plan(c, nil, nil, linuxOnly)
	if len(probes) != 2 || probes[0].Environment.OS != "linux" {
		t.Fatalf("linux worker got %+v", probes)
	}
	// With the linux pair run and windows out of reach, the linux worker
	// walks the boundary instead of being handed windows forever.
	fp := strings.Repeat("a", 64)
	runs := []Run{{Version: "2.4.0", Environment: DefaultEnvironment, Verdict: VerdictFail, FailureFingerprint: fp}, {Version: "2.4.1", Environment: DefaultEnvironment, Verdict: VerdictPass}}
	probes = Plan(c, runs, []string{"2.3.9", "2.4.0", "2.4.1"}, linuxOnly)
	if len(probes) != 1 || probes[0].Version != "2.3.9" || probes[0].Environment.OS != "linux" {
		t.Fatalf("linux walk = %+v", probes)
	}
}

func TestResolvePrefersAnExistingSampleThenUpstreamThenGenerated(t *testing.T) {
	c := goodCandidate()
	samples := []SampleCandidate{
		{SampleID: "sha256:other", ManifestJSON: `{"packages":["pkg:npm/bar@1.0.0"],"symbols":["parse"]}`},
		{SampleID: "sha256:how", ManifestJSON: `{"packages":["pkg:npm/foo@2.3.0"],"symbols":["serialize"],"case":{"kind":"HOW"}}`},
		{SampleID: "sha256:match", ManifestJSON: `{"packages":["pkg:npm/foo@2.3.0"],"symbols":["foo.parse"],"case":{"kind":"HOW"}}`},
	}
	res := Resolve(c, samples)
	if res.Source != ReproducerExistingSample || !reflect.DeepEqual(res.ExistingSamples, []string{"sha256:match"}) {
		t.Fatalf("%+v", res)
	}
	c.Symbols = nil
	if res := Resolve(c, samples); res.Source != ReproducerGenerated {
		t.Fatalf("symbol-less claim reused a HOW sample: %+v", res)
	}
	c.UpstreamReproducer = true
	if res := Resolve(c, samples); res.Source != ReproducerUpstream {
		t.Fatalf("%+v", res)
	}
	fixSample := []SampleCandidate{{SampleID: "sha256:fix", ManifestJSON: `{"packages":["pkg:npm/foo@2.3.0"],"case":{"kind":"FIX"}}`}}
	if res := Resolve(c, fixSample); res.Source != ReproducerExistingSample {
		t.Fatalf("a FIX sample for the package was not reused: %+v", res)
	}
}

func TestAGYOutputIsSchemaBoundedAndCannotCarryAStatus(t *testing.T) {
	good := `{"schemaVersion":1,"ecosystem":"pypi","name":"requests","claimedBadVersion":"2.32.3","claimedFixedVersion":"2.32.4","claim":"Session.get raised UnicodeDecodeError on non-UTF-8 redirect locations","sourceUrl":"https://github.com/psf/requests/releases/tag/v2.32.4","sourceType":"release_note","symbols":["Session.get"],"confidence":"high"}`
	c, err := ParseAGYOutput([]byte("```json\n" + good + "\n```"))
	if err != nil || c.Name != "requests" {
		t.Fatalf("%v %+v", err, c)
	}
	withStatus := strings.TrimSuffix(good, "}") + `,"status":"VERIFIED_FIX"}`
	if _, err := ParseAGYOutput([]byte(withStatus)); err == nil {
		t.Fatal("a status field was accepted")
	}
	if _, err := ParseAGYOutput([]byte(`{"decline":true,"reason":"docs only"}`)); err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("decline = %v", err)
	}
	if _, err := ParseAGYOutput([]byte(good + good)); err == nil {
		t.Fatal("two documents accepted")
	}
	invalid := strings.Replace(good, `"confidence":"high"`, `"confidence":"sure"`, 1)
	if _, err := ParseAGYOutput([]byte(invalid)); err == nil {
		t.Fatal("validator bypassed")
	}
	prompt := AGYPrompt(AGYInput{Source: Source{Ecosystem: "pypi", Name: "requests", Repo: "psf/requests"}, Release: "2.32.4", Line: "fix: ..."})
	if !strings.Contains(prompt, "Never include a status") || !strings.Contains(prompt, `"decline":true`) {
		t.Fatal("prompt lost a rule")
	}
}

func TestScoreWeighsExistingEvidenceAboveEverything(t *testing.T) {
	c := goodCandidate()
	now := time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)
	c.ReleasedAt = now.Add(-10 * 24 * time.Hour)
	base := Score(c, ScoreInputs{Now: now})
	if Score(c, ScoreInputs{Now: now, FingerprintMatch: true}) <= base+scoreSampleReuse {
		t.Fatal("a fingerprint match should outrank a sample reuse")
	}
	c.Confidence = ConfidenceLow
	if Score(c, ScoreInputs{Now: now}) >= base {
		t.Fatal("confidence did not order")
	}
	quiet := goodCandidate()
	quiet.Claim = "Fixed behaviour of parse for empty input"
	if Score(quiet, ScoreInputs{}) >= Score(goodCandidate(), ScoreInputs{}) {
		t.Fatal("a crash should outrank a quiet fix")
	}
	if Score(goodCandidate(), ScoreInputs{Asks: 1000}) != Score(goodCandidate(), ScoreInputs{Asks: 5}) {
		t.Fatal("asks are not capped")
	}
}

func TestMeasureReportsPhase0Rates(t *testing.T) {
	states := []CandidateState{
		{Status: StatusVerifiedFix, PairOutcome: PairReproducedAndFixed, Attempts: 1, ReproducerSource: ReproducerExistingSample, RunCount: 2, FarmSeconds: 120},
		{Status: StatusReproducedBug, PairOutcome: PairReproducedNotFixed, Attempts: 1, ReproducerSource: ReproducerGenerated, RunCount: 2, FarmSeconds: 200},
		{Status: StatusClaimNotReproduced, PairOutcome: PairNotReproduced, Attempts: 2, ReproducerSource: ReproducerGenerated, RunCount: 2, FarmSeconds: 80},
		{Status: StatusClaimedFix, Attempts: 3, Exhausted: true},
		{Status: StatusClaimedFix},
	}
	m := Measure(states, 12, 7)
	if m.Accepted != 5 || m.Attempted != 4 || m.WithReproducer != 3 || m.Reproduced != 2 || m.FixConfirmed != 1 || m.NotFixed != 1 || m.NotReproduced != 1 || m.Exhausted != 1 {
		t.Fatalf("%+v", m)
	}
	if m.ExtractionPrecision != 5.0/12 || m.ReproducerRate != 0.75 || m.ReproducedRate != 2.0/3 || m.ConfirmedRate != 0.5 || m.IncorrectClaimRate != 0.5 || m.ReuseRate != 1.0/3 {
		t.Fatalf("rates: %+v", m)
	}
	if m.FarmSecondsPerVerifiedFix != 400 {
		t.Fatalf("cost = %v", m.FarmSecondsPerVerifiedFix)
	}
	if Measure(nil, 0, 0).ConfirmedRate != 0 {
		t.Fatal("empty measure divided by zero")
	}
}
