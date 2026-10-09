package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"testing"
)

func TestFarm202RepeatedIssuePageDoesNotRebuildCompleteLedger(t *testing.T) {
	mux, f := newTestMux(t, nil)
	raw := make([]string, 600)
	for i := range raw {
		b, err := json.Marshal(failureCluster{Stage: "PROJECT_TEST", Fingerprint: fmt.Sprintf("sha256:issue-%04d", i), EvidenceQuality: "complete", ErrorSummary: "unchanged issue", Count: int64(i + 1), Versions: []string{"1.3.0"}, EnvSummary: map[string]string{"os": "linux", "runtime": "node@22"}})
		if err != nil {
			t.Fatal(err)
		}
		raw[i] = string(b)
	}
	f.issueClusters["npm|ledgerish"] = raw
	id := failureIssueID("fp|sha256:issue-0000")
	path := "/npm/ledgerish?issue=" + id
	if r := get(t, mux, path); r.Code != http.StatusOK {
		t.Fatalf("warm issue status=%d", r.Code)
	}
	allocations := testing.AllocsPerRun(5, func() {
		if r := get(t, mux, path); r.Code != http.StatusOK {
			panic("same issue disappeared")
		}
	})
	t.Logf("repeated complete issue-page allocations=%.0f", allocations)
	if allocations > 2000 {
		t.Fatalf("unchanged complete ledger rebuilt for every issue request: %.0f allocations", allocations)
	}
}

func TestFarm202CompleteIssueCacheKeepsDisplayAndCompleteLedgersSeparate(t *testing.T) {
	var s site
	raw := []string{
		"{\"stage\":\"PROJECT_TEST\",\"errorFp\":\"sha256:display\",\"fingerprint\":\"sha256:display\",\"evidenceQuality\":\"complete\",\"count\":3}",
		"{\"stage\":\"PROJECT_TEST\",\"fingerprint\":\"sha256:outside-display\",\"evidenceQuality\":\"complete\",\"count\":7}",
	}
	display, err := s.decodedClusters(context.Background(), "npm", "lib", raw[:1])
	if err != nil || len(display) != 1 {
		t.Fatal("display load", err)
	}
	issues, err := s.currentFailureIssues(context.Background(), "npm", "lib", raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := failureIssueByID(issues, failureIssueID("fp|sha256:outside-display")); !ok {
		t.Fatal("complete issue beyond display cap disappeared")
	}
	again, err := s.decodedClusters(context.Background(), "npm", "lib", raw[:1])
	if err != nil || len(again) != 1 {
		t.Fatal("complete ledger leaked into display", err)
	}
	if len(s.clusterCache.entries) != 2 {
		t.Fatal("complete and display cache identity collided")
	}
}

func TestFarm202IssuePageSeesChangedAndRemovedCurrentDocuments(t *testing.T) {
	mux, f := newTestMux(t, nil)
	clusters := seedFailureIssueFixture(t, f)
	key := "npm|libx"
	id := issueIDFor(t, clusters, clusters[0].Fingerprint)
	path := "/npm/libx?issue=" + id
	if r := get(t, mux, path); r.Code != http.StatusOK {
		t.Fatal("initial issue unavailable")
	}
	var changed failureCluster
	if err := json.Unmarshal([]byte(f.clusters[key][0]), &changed); err != nil {
		t.Fatal(err)
	}
	changed.ErrorSummary = "current source changed immediately"
	changed.Count = 91
	b, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	f.issueClusters[key] = []string{string(b), f.clusters[key][1]}
	rec := get(t, mux, path)
	if rec.Code != http.StatusOK {
		t.Fatal("changed issue unavailable")
	}
	mustContain(t, rec.Body.String(), "current source changed immediately")
	f.issueClusters[key] = f.clusters[key][1:]
	if r := get(t, mux, path); r.Code != http.StatusNotFound {
		t.Fatal("removed issue served from stale cache")
	}
}

func TestFarm202CompleteIssueCacheSharesExistingRetentionBudget(t *testing.T) {
	var s site
	s.clusterCache.maxEntries = 2
	raw := []string{"{\"stage\":\"PROJECT_TEST\",\"fingerprint\":\"sha256:budget\",\"evidenceQuality\":\"complete\",\"count\":3}"}
	if _, err := s.decodedClusters(context.Background(), "npm", "display-a", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.currentFailureIssues(context.Background(), "npm", "complete", raw); err != nil {
		t.Fatal(err)
	}
	if _, err := s.decodedClusters(context.Background(), "npm", "display-b", raw); err != nil {
		t.Fatal(err)
	}
	if len(s.clusterCache.entries) != 2 || s.clusterCache.bytes > clusterDecodeBudget {
		t.Fatal("issue cache escaped existing group/byte budget")
	}
	if _, ok := s.clusterCache.entries["npm|display-a"]; ok {
		t.Fatal("oldest display group was not evicted")
	}
	var n int64
	for _, el := range s.clusterCache.entries {
		n += el.Value.(*decodedClusterEntry).bytes
	}
	if n != s.clusterCache.bytes {
		t.Fatal("shared issue budget accounting drift")
	}
}

func TestFarm202OversizedCompleteIssuesReturnEveryIdentityWithoutRetention(t *testing.T) {
	var s site
	s.clusterCache.maxBytes = 1024
	raw := make([]string, 700)
	for i := range raw {
		raw[i] = fmt.Sprintf("{\"stage\":\"PROJECT_TEST\",\"fingerprint\":\"sha256:complete-%04d\",\"evidenceQuality\":\"complete\",\"count\":3}", i)
	}
	expected := buildFailureIssues(decodeFailureClusters(raw))
	for i := 0; i < 2; i++ {
		got, err := s.currentFailureIssues(context.Background(), "npm", "wide", raw)
		if err != nil || !reflect.DeepEqual(got, expected) {
			t.Fatalf("oversized complete ledger omitted identities: len=%d err=%v", len(got), err)
		}
		if len(s.clusterCache.entries) != 0 || s.clusterCache.bytes != 0 {
			t.Fatal("oversized complete result retained")
		}
	}
}

func TestFarm202ConcurrentCompleteIssueViewsDoNotMutateSharedFacts(t *testing.T) {
	var s site
	raw := []string{"{\"stage\":\"PROJECT_TEST\",\"fingerprint\":\"sha256:immutable\",\"evidenceQuality\":\"complete\",\"count\":9,\"versions\":[\"1.3.0\",\"1.4.0\"],\"envSummary\":{\"os\":\"linux\"}}"}
	expected := buildFailureIssues(decodeFailureClusters(raw))
	got, err := s.currentFailureIssues(context.Background(), "npm", "immutable", raw)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				issues, err := s.currentFailureIssues(context.Background(), "npm", "immutable", raw)
				if err != nil || len(issues) != 1 {
					t.Error("concurrent complete issue unavailable")
					return
				}
				issue := issues[0]
				verdicts := failureIssueVerdicts(issue, []string{"1.4.0", "1.3.0", "1.2.0"}, map[string]int64{"1.2.0": 8})
				_ = failureIssueGaps("en", issue, verdicts, nil)
				_ = failureIssueDescription("en", "immutable", issue)
			}
		}()
	}
	wg.Wait()
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("request view changed retained complete issue facts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.currentFailureIssues(ctx, "npm", "immutable", raw); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled warm issue request did not leave")
	}
}
