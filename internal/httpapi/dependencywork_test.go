package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// scriptedRegistry is a public registry that answers from a script and counts
// how many times it was asked. The count is the point: this endpoint is polled
// several times a minute by every worker in the fleet, and each dependency
// coordinate it has not seen before is an outbound request to somebody else's
// registry.
type scriptedRegistry struct {
	mu       sync.Mutex
	verdicts map[string]string
	calls    int
}

func (c *scriptedRegistry) Check(_ context.Context, p domain.PURL) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if status, ok := c.verdicts[p.String()]; ok {
		return status
	}
	return scanner.PublicnessUnknown
}

func (c *scriptedRegistry) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// dependencyServer is the default harness with the publicness gate ON. The
// shared helper runs trust mode, which is exactly the configuration this gate
// is skipped in.
func dependencyServer(t *testing.T, checker PublicnessChecker) (*httptest.Server, *serverstore.Fake) {
	t.Helper()
	var store *serverstore.Fake
	srv, s, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.PublicCheck = ""
		d.Checker = checker
	})
	store = s
	return srv, store
}

// seedResolvedDependency records one project that chose parent and whose
// lockfile resolved it onto child. Nothing ever reports child on its own,
// which is exactly what makes it invisible to every other queue source.
func seedResolvedDependency(t *testing.T, store *serverstore.Fake, parent, child string) {
	t.Helper()
	p, err := domain.ParsePURL(parent)
	if err != nil {
		t.Fatal(err)
	}
	if _, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{{
		SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "peer-dep", ProjectBucket: "proj-dep",
		Package: parent, Direct: true, DependsOn: []string{child},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 7,
		Environment: nodeEnv("esm"),
	}}); err != nil || len(rejected) != 0 {
		t.Fatalf("ingest: rejected=%v err=%v", rejected, err)
	}
	if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{
		PURL: p.String(), Ecosystem: p.Ecosystem, Name: p.Name, Version: p.Version,
		Major: p.Major(), Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
}

func askForWork(t *testing.T, srv, token string) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+"/v1/authoring/work/next",
		bytes.NewBufferString(`{"schemaVersion":1,"sandboxCapability":"CONTAINER_RUN","verifierOS":["linux"],"clientVersion":"v0.1.22"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

const dependencyToken = "csx_author_v1_YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE"

// writerToken mints a distinct, correctly shaped session token. The bearer
// must decode to exactly 32 raw bytes, so a token cannot be varied by
// appending to it.
func writerToken(n byte) string {
	raw := bytes.Repeat([]byte{'a'}, 32)
	raw[31] = 'a' + n
	return "csx_author_v1_" + base64.RawURLEncoding.EncodeToString(raw)
}

// The end of the loop the issue describes: a coordinate the network holds no
// evidence for, reachable only through a resolved lockfile edge, becomes a job
// a worker is actually handed.
func TestAResolvedDependencyIsHandedOutOnceTheRegistryConfirmsIt(t *testing.T) {
	checker := &scriptedRegistry{verdicts: map[string]string{
		"pkg:npm/body-parser@2.2.0": scanner.PublicnessPublic,
	}}
	srv, store := dependencyServer(t, checker)
	authoringSession(t, store, dependencyToken, "writer-dep", testNow)
	seedResolvedDependency(t, store, "pkg:npm/express@5.1.0", "pkg:npm/body-parser@2.2.0")

	got := askForWork(t, srv.URL, dependencyToken)
	work, _ := got["work"].(map[string]any)
	if got["status"] != "ASSIGNED" || work == nil {
		t.Fatalf("work = %v, want an assignment", got)
	}
	// express itself is observed and unproven, so it is the higher-ranked hole
	// and the first writer gets it. The dependency is what the fleet reaches
	// next, and reaching it is the whole point: nothing else in the queue can
	// ever produce that coordinate.
	seen := []string{work["package"].(string)}
	for i := byte(1); i <= 4 && work["kind"] != "DEPENDENCY"; i++ {
		token := writerToken(i)
		authoringSession(t, store, token, "writer-dep-"+string(rune('a'+i)), testNow)
		got = askForWork(t, srv.URL, token)
		if got["status"] != "ASSIGNED" {
			t.Fatalf("poll %d = %v, want an assignment; assignments so far were %v", i, got, seen)
		}
		work, _ = got["work"].(map[string]any)
		seen = append(seen, work["package"].(string))
	}
	if work["kind"] != "DEPENDENCY" || work["package"] != "pkg:npm/body-parser@2.2.0" {
		t.Fatalf("the resolved dependency was never offered; assignments were %v", seen)
	}
}

// A dependency the registry will not confirm must not be handed out -- an
// automatic build target has to be a package that exists publicly (absolute
// principle 2) -- and refusing it must not stop the rest of the queue. This is
// the issue's fifth scenario: one dependency withheld, every other queue still
// moving.
func TestAnUnconfirmedDependencyIsWithheldWhileTheQueueKeepsMoving(t *testing.T) {
	// The registry answers for nothing: an outage looks exactly like this.
	checker := &scriptedRegistry{verdicts: map[string]string{}}
	srv, store := dependencyServer(t, checker)
	authoringSession(t, store, dependencyToken, "writer-dep", testNow)
	seedResolvedDependency(t, store, "pkg:npm/express@5.1.0", "pkg:npm/body-parser@2.2.0")

	for i := 0; i < 3; i++ {
		got := askForWork(t, srv.URL, dependencyToken)
		if got["status"] == "NO_WORK" {
			continue
		}
		work, _ := got["work"].(map[string]any)
		if work["kind"] == "DEPENDENCY" {
			t.Fatalf("handed out %v without the registry confirming it exists", work["package"])
		}
		// express, the observed hole, is still offered. The withheld
		// dependency did not take the queue down with it.
		if work["package"] != "pkg:npm/express@5.1.0" {
			t.Fatalf("unexpected assignment %v", work)
		}
	}
	// And the coordinate is retried rather than written off. An unanswered
	// probe is not a verdict, so a registry having a bad afternoon must not
	// become a permanent exclusion.
	if checker.count() < 2 {
		t.Errorf("the registry was asked %d times across three polls; an unanswered probe must be retried", checker.count())
	}
}

// An unanswered probe is not a verdict. Caching it would turn one bad
// afternoon at npmjs.org into a permanent exclusion, so the coordinate is
// retried -- and because it is retried, the retry has to be bounded or the
// fleet's poll loop becomes a sustained load on somebody else's registry.
func TestDependencyConfirmationIsBoundedPerRequest(t *testing.T) {
	checker := &scriptedRegistry{verdicts: map[string]string{}}
	srv, store := dependencyServer(t, checker)
	authoringSession(t, store, dependencyToken, "writer-dep", testNow)
	for _, child := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		seedResolvedDependency(t, store,
			"pkg:npm/parent-"+child+"@1.0.0", "pkg:npm/dep-"+child+"@1.0.0")
	}

	askForWork(t, srv.URL, dependencyToken)
	got := checker.count()
	if got == 0 {
		t.Fatal("no coordinate was confirmed at all; the bound cannot be read from a path that never runs")
	}
	if got > maxDependencyProbesPerRequest {
		t.Errorf("one poll made %d registry probes, want at most %d", got, maxDependencyProbesPerRequest)
	}
}

// A server with the gate on and no checker cannot tell whether a dependency
// coordinate is public, and UNKNOWN is private -- the safe default the whole
// evidence boundary already uses.
func TestWithoutACheckerNoDependencyWorkIsOffered(t *testing.T) {
	srv, store := dependencyServer(t, nil)
	authoringSession(t, store, dependencyToken, "writer-dep", testNow)
	seedResolvedDependency(t, store, "pkg:npm/express@5.1.0", "pkg:npm/body-parser@2.2.0")

	for i := 0; i < 3; i++ {
		got := askForWork(t, srv.URL, dependencyToken)
		if work, ok := got["work"].(map[string]any); ok && work["kind"] == "DEPENDENCY" {
			t.Fatalf("offered %v with no way to confirm it is public", work["package"])
		}
	}
}
