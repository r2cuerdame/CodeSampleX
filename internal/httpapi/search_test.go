package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func saveTestSample(t *testing.T, store *serverstore.Fake, status string) string {
	t.Helper()
	manifest := testManifest()
	sampleID := "sha256:" + strings.Repeat("42", 32)
	if err := store.SaveSample(context.Background(), serverstore.SampleRow{
		SampleID:     sampleID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       status,
		License:      "MIT-0",
		SizeBytes:    512,
		CreatedAt:    testNow,
	}); err != nil {
		t.Fatal(err)
	}
	return sampleID
}

func TestSearchModuleSystemMismatchIsAdaptationRequired(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	saveTestSample(t, store, "PUBLISHED") // sample env: esm

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post JSON with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Symbols:       []string{"axios.post"},
		Environment:   nodeEnv("cjs"), // requester uses CommonJS
	}
	var out domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v1/search", req, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if out.Miss || len(out.Results) == 0 {
		t.Fatalf("miss=%v results=%d, want a hit", out.Miss, len(out.Results))
	}
	r := out.Results[0]
	if r.Grade != domain.GradeAdaptationRequired {
		t.Fatalf("grade = %s, want ADAPTATION_REQUIRED", r.Grade)
	}
	foundAdaptation := false
	for _, a := range r.Adaptation {
		if strings.Contains(a, "cjs") {
			foundAdaptation = true
		}
	}
	if !foundAdaptation {
		t.Fatalf("adaptation = %v, want an import-syntax entry", r.Adaptation)
	}
	if r.Case == nil || r.Case.Goal == "" {
		t.Fatal("result must carry the case")
	}
}

func TestSearchExactEnvironmentIsExactGrade(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	saveTestSample(t, store, "PUBLISHED")

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post JSON with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Symbols:       []string{"axios.post"},
		Environment:   nodeEnv("esm"),
	}
	var out domain.SearchResponse
	postJSON(t, srv.URL+"/v1/search", req, &out)
	if out.Miss || len(out.Results) == 0 {
		t.Fatalf("want a hit, got miss=%v", out.Miss)
	}
	if out.Results[0].Grade != domain.GradeExact {
		t.Fatalf("grade = %s, want EXACT", out.Results[0].Grade)
	}
}

func TestSearchUnrelatedQueryIsNoSafeMatch(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	saveTestSample(t, store, "PUBLISHED")

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "quantum blockchain teapot orchestration",
		Environment:   nodeEnv("esm"),
	}
	var out domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v1/search", req, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !out.Miss || len(out.Results) != 0 {
		t.Fatalf("miss=%v results=%d, want NO_SAFE_MATCH miss", out.Miss, len(out.Results))
	}
}

func TestSearchElevatedFailureInRequesterContextIsReferenceOnly(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	saveTestSample(t, store, "PUBLISHED")

	// Materialize a snapshot with an ELEVATED_FAILURE row in the requester's
	// context (node 22.18, cjs env is irrelevant — context label decides).
	env := nodeEnv("esm")
	fp := "sha256:" + strings.Repeat("ab", 32)
	rows := []serverstore.EvidenceRow{
		{
			PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post",
			EnvHash: env.Normalize().Hash(),
			EnvJSON: string(domain.MustCanonicalJSON(env.Normalize())),
			Stage:   "PROJECT_COMPILE", Result: "PASS", ObservationCount: 3,
			UniquePeerBuckets: 2, LastSeen: testNow,
		},
		{
			PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post",
			EnvHash: env.Normalize().Hash(),
			EnvJSON: string(domain.MustCanonicalJSON(env.Normalize())),
			Stage:   "PROJECT_COMPILE", Result: "FAIL", ObservationCount: 4,
			ErrorFingerprint: fp, ErrorCode: "ERR_REQUIRE_ESM",
			UniquePeerBuckets: 2, LastSeen: testNow,
		},
	}
	snap := compatibility.BuildSnapshot("pkg:npm/axios@1.12.0", "axios.post", rows, nil, nil, testNow)
	js, _ := json.Marshal(snap)
	if err := store.PutSnapshot(context.Background(), "pkg:npm/axios@1.12.0", "axios.post", string(js)); err != nil {
		t.Fatal(err)
	}

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         "post JSON with axios",
		Packages:      []string{"pkg:npm/axios@1.12.0"},
		Symbols:       []string{"axios.post"},
		Environment:   nodeEnv("esm"),
	}
	var out domain.SearchResponse
	postJSON(t, srv.URL+"/v1/search", req, &out)
	if out.Miss || len(out.Results) == 0 {
		t.Fatalf("want a (demoted) hit, got miss=%v", out.Miss)
	}
	r := out.Results[0]
	if r.Grade != domain.GradeReferenceOnly {
		t.Fatalf("grade = %s, want REFERENCE_ONLY (elevated failure in requester context)", r.Grade)
	}
	if len(r.Evidence.ElevatedFailures) == 0 {
		t.Fatal("evidence summary must name the elevated-failure context")
	}
}

func TestSearchVerifiedSampleOutranksUnverified(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	ctx := context.Background()

	manifest := testManifest()
	unverified := "sha256:" + strings.Repeat("aa", 32)
	verified := "sha256:" + strings.Repeat("bb", 32)
	for _, s := range []struct {
		id, status string
	}{{unverified, "PUBLISHED"}, {verified, "CROSS_PASS"}} {
		if err := store.SaveSample(ctx, serverstore.SampleRow{
			SampleID:     s.id,
			ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
			Status:       s.status, License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
		}); err != nil {
			t.Fatal(err)
		}
	}

	req := domain.SearchRequest{
		SchemaVersion: 1, Query: "post JSON with axios",
		Packages:    []string{"pkg:npm/axios@1.12.0"},
		Environment: nodeEnv("esm"),
		Limit:       2,
	}
	var out domain.SearchResponse
	postJSON(t, srv.URL+"/v1/search", req, &out)
	if len(out.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(out.Results))
	}
	if out.Results[0].SampleID != verified {
		t.Fatalf("top result = %s (status %s), want the CROSS_PASS sample first",
			out.Results[0].SampleID, out.Results[0].SampleStatus)
	}
	if out.Results[0].Score <= out.Results[1].Score {
		t.Fatalf("verified score %v must beat unverified %v",
			out.Results[0].Score, out.Results[1].Score)
	}
}

// --- shards -------------------------------------------------------------------

func TestShardETagAnd304(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	if err := store.PutShard(context.Background(), "npm/axios/1",
		"deadbeef", `{"schemaVersion":1,"key":"npm/axios/1","packages":[]}`); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(srv.URL + "/v1/shards/npm/axios/1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	if etag != `"deadbeef"` {
		t.Fatalf("etag = %q", etag)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/shards/npm/axios/1", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp.StatusCode)
	}

	// Unknown shard → 404.
	resp, _ = http.Get(srv.URL + "/v1/shards/npm/nope/1")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestSearchRejectsPackageOnlyRelevance pins the wrong-HIT reproduced on
// production: POST /v1/search with "how to bake a chocolate cake" and
// google/uuid in the request returned the UUID sample as MATCH: EXACT at
// score 0.84. Naming any package scored 0.35 by itself — past
// noSafeMatchThreshold before the ×3 verification multiplier — and the
// relevance guard only ran when NO package was given.
func TestSearchRejectsPackageOnlyRelevance(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	manifest := testManifest()
	manifest.Case.Goal = "Generate, parse and validate a UUID in Go with google/uuid"
	manifest.Case.Packages = []string{"pkg:npm/axios@1.12.0"}
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID:     "sha256:" + strings.Repeat("ee", 32),
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       "MATRIX_PASS", // strongest multiplier: the worst case
		License:      "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	ask := func(query string) (bool, string) {
		var resp struct {
			Miss    bool `json:"miss"`
			Results []struct {
				Grade string  `json:"match"`
				Score float64 `json:"score"`
			} `json:"results"`
		}
		postJSON(t, srv.URL+"/v1/search", map[string]any{
			"schemaVersion": 1, "query": query,
			"packages":    []string{"pkg:npm/axios@1.12.0"},
			"environment": nodeEnv("esm"),
		}, &resp)
		if resp.Miss || len(resp.Results) == 0 {
			return true, ""
		}
		return false, resp.Results[0].Grade
	}

	if miss, grade := ask("how to bake a chocolate cake"); !miss {
		t.Errorf("unrelated query hit on package overlap alone (grade %s)", grade)
	}
	// The same sample must still answer a question it is actually about.
	if miss, _ := ask("validate a uuid"); miss {
		t.Error("the relevance gate also blocked an on-topic query")
	}
}

// TestQuarantinedSampleIsNotServed pins the takedown path. Publishing is
// anonymous and permanent, so without this a single abusive or mistaken
// sample could never be removed — there is no delete, no TTL and no admin
// API anywhere in the system.
func TestQuarantinedSampleIsNotServed(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)

	manifest := testManifest()
	manifest.Case.Goal = "post JSON with axios"
	id := "sha256:" + strings.Repeat("aa", 32)
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{
		SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status: "MATRIX_PASS", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
	}); err != nil {
		t.Fatal(err)
	}

	find := func() bool {
		var resp struct {
			Miss    bool `json:"miss"`
			Results []struct {
				SampleID string `json:"sampleId"`
			} `json:"results"`
		}
		postJSON(t, srv.URL+"/v1/search", map[string]any{
			"schemaVersion": 1, "query": "post json with axios",
			"packages":    []string{"pkg:npm/axios@1.12.0"},
			"environment": nodeEnv("esm"),
		}, &resp)
		for _, r := range resp.Results {
			if r.SampleID == id {
				return true
			}
		}
		return false
	}

	if !find() {
		t.Fatal("sample not findable before quarantine; the test proves nothing")
	}
	if err := store.SetSampleQuarantine(t.Context(), id, true, "abuse"); err != nil {
		t.Fatal(err)
	}
	if find() {
		t.Error("a quarantined sample is still served by search")
	}
	// Search was the only path this test used to check, which is how the
	// direct-fetch endpoints kept serving a withdrawn sample: by content
	// address, with its old status and no sign it had been taken down.
	// "Not served" has to mean every serving read, or a quarantine only
	// hides a sample from people who did not already have its id.
	for _, path := range []string{
		"/v1/samples/" + id,
		"/v1/samples/" + id + "/artifact",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d while quarantined, want 404", path, resp.StatusCode)
		}
	}
	// Reversible: the evidence trail was hidden, not destroyed.
	if err := store.SetSampleQuarantine(t.Context(), id, false, ""); err != nil {
		t.Fatal(err)
	}
	if !find() {
		t.Error("releasing a quarantine did not restore the sample")
	}
	resp, err := http.Get(srv.URL + "/v1/samples/" + id)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET the sample after release = %d, want 200", resp.StatusCode)
	}
}
