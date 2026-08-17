package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestSearchV1AndV2RollingWireContracts(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	id := "sha256:" + strings.Repeat("8", 64)
	saveSearchFixture(t, store, id, "post JSON with axios", "pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"))

	// Old request -> new server. This is the exact original v1 field set.
	oldRequest := `{"schemaVersion":1,"query":"post JSON with axios","packages":["pkg:npm/axios@1.12.0"],` +
		`"symbols":["axios.post"],"environment":{"schemaVersion":1,"ecosystem":"npm","os":"windows",` +
		`"arch":"amd64","runtime":"node","runtimeVersion":"22.18.1","moduleSystem":"esm"}}`
	v1Raw := postRawSearch(t, srv.URL+"/v1/search", oldRequest)
	var v1 map[string]any
	if err := json.Unmarshal(v1Raw, &v1); err != nil {
		t.Fatal(err)
	}
	if v1["schemaVersion"] != float64(1) || v1["miss"] != false {
		t.Fatalf("old request was not served as v1: %s", v1Raw)
	}
	assertStrictOldResponseShape(t, v1)

	// Negotiated v2 keeps the new evidence bit. It is present even when false
	// so callers can distinguish "negotiated and did not match" from v1 where
	// the capability was unavailable.
	newRequest := strings.Replace(oldRequest, `"schemaVersion":1`, `"schemaVersion":2`, 1)
	v2Raw := postRawSearch(t, srv.URL+"/v2/search", newRequest)
	var v2 map[string]any
	if err := json.Unmarshal(v2Raw, &v2); err != nil {
		t.Fatal(err)
	}
	if v2["schemaVersion"] != float64(2) || v2["miss"] != false {
		t.Fatalf("v2 negotiation failed: %s", v2Raw)
	}
	results := v2["results"].([]any)
	if _, ok := results[0].(map[string]any)["exactFailureMatched"]; !ok {
		t.Fatalf("v2 lost exactFailureMatched: %s", v2Raw)
	}

	var decoded domain.SearchResponse
	if err := json.Unmarshal(v1Raw, &decoded); err != nil || decoded.SchemaVersion != 1 || decoded.Results[0].ExactFailureMatched {
		t.Fatalf("new client could not consume old v1 response: err=%v response=%+v", err, decoded)
	}
}

func postRawSearch(t *testing.T, url, body string) []byte {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s status=%d body=%s", url, resp.StatusCode, raw)
	}
	return raw
}

func assertStrictOldResponseShape(t *testing.T, response map[string]any) {
	t.Helper()
	allowedTop := map[string]bool{"schemaVersion": true, "results": true, "miss": true}
	for key := range response {
		if !allowedTop[key] {
			t.Fatalf("old additionalProperties:false response validator rejects top-level %q", key)
		}
	}
	allowedResult := map[string]bool{
		"match": true, "confidence": true, "score": true, "case": true, "sampleId": true,
		"sampleStatus": true, "exact": true, "different": true, "adaptationNeeded": true,
		"evidence": true, "knownFailures": true,
	}
	for _, item := range response["results"].([]any) {
		for key := range item.(map[string]any) {
			if !allowedResult[key] {
				t.Fatalf("old additionalProperties:false result validator rejects %q", key)
			}
		}
	}
}
