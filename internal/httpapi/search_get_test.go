package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// GET /v2/search (#318). A browser, a cloud agent that can only fetch a
// URL, or a person with curl can ask the same question the JSON body asks,
// with the fields spelled as query parameters. It is the SAME pipeline --
// the same grader, the same miss threshold, the same v2 shape -- reached by
// a second door; nothing here grades differently because it arrived as a
// query string.
func TestGetSearchIsTheSameAnswerAsPostV2(t *testing.T) {
	srv, store, _ := newTestServer(t, func(d *Deps) { d.Cfg.PublicURL = "https://csx.example" })
	id := "sha256:" + strings.Repeat("9", 64)
	saveSearchFixture(t, store, id, "post JSON with axios", "pkg:npm/axios@1.12.0", "axios.post", nodeEnv("esm"))

	posted := postRawSearch(t, srv.URL+"/v2/search",
		`{"schemaVersion":2,"query":"post JSON with axios","packages":["pkg:npm/axios@1.12.0"],`+
			`"symbols":["axios.post"],"environment":{"schemaVersion":1,"ecosystem":"npm","os":"windows",`+
			`"arch":"amd64","runtime":"node","runtimeVersion":"22.18.1","moduleSystem":"esm"}}`)
	fetched := getRawSearch(t, srv.URL+"/v2/search?q=post+JSON+with+axios&package=pkg:npm/axios@1.12.0"+
		"&symbol=axios.post&ecosystem=npm&os=windows&arch=amd64&runtime=node&runtimeVersion=22.18.1&moduleSystem=esm")

	var viaPost, viaGet map[string]any
	if err := json.Unmarshal(posted, &viaPost); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(fetched, &viaGet); err != nil {
		t.Fatal(err)
	}
	if viaGet["schemaVersion"] != float64(2) || viaGet["miss"] != false {
		t.Fatalf("GET was not served as a v2 hit: %s", fetched)
	}
	for _, key := range []string{"grade", "miss", "schemaVersion"} {
		if viaGet[key] != viaPost[key] {
			t.Errorf("%s: GET %v != POST %v", key, viaGet[key], viaPost[key])
		}
	}
	gotResults, postResults := viaGet["results"].([]any), viaPost["results"].([]any)
	if len(gotResults) != len(postResults) {
		t.Fatalf("GET returned %d results, POST %d", len(gotResults), len(postResults))
	}
	top, want := gotResults[0].(map[string]any), postResults[0].(map[string]any)
	for _, key := range []string{"match", "sampleId", "sampleUrl", "score", "confidence"} {
		if top[key] != want[key] {
			t.Errorf("results[0].%s: GET %v != POST %v", key, top[key], want[key])
		}
	}
	if top["sampleUrl"] != "https://csx.example/samples/"+id {
		t.Errorf("sampleUrl = %v", top["sampleUrl"])
	}

	// A miss through the same door is spelled the same way.
	miss := getRawSearch(t, srv.URL+"/v2/search?q=left+pad&package=pkg:npm/left-pad@1.3.0")
	var m map[string]any
	if err := json.Unmarshal(miss, &m); err != nil {
		t.Fatal(err)
	}
	if m["miss"] != true || m["grade"] != "NO_SAFE_MATCH" {
		t.Fatalf("GET miss is not spelled NO_SAFE_MATCH: %s", miss)
	}
}

// A GET with nothing to search for is a request, not a listing. It is
// refused with a sentence that names the parameters, so the URL a reader
// guessed at teaches them the ones that exist.
func TestGetSearchWithNoQuestionIsRefused(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/v2/search")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	for _, name := range []string{"q", "package", "symbol", "errorCode"} {
		if !strings.Contains(string(body), name) {
			t.Errorf("refusal does not name the %q parameter: %s", name, body)
		}
	}
	// Limit is bounded the same way the body form bounds it; a huge or
	// negative value is not an error, it is the default.
	resp, err = http.Get(srv.URL + "/v2/search?q=anything&limit=-5")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("limit=-5 status = %d", resp.StatusCode)
	}
}

// The frozen v1 surface does not grow a GET form: v1 is the byte shape old
// clients pinned, and a new door onto it would be a new contract to freeze.
func TestGetSearchIsV2Only(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp, err := http.Get(srv.URL + "/v1/search?q=anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/search status = %d, want 405", resp.StatusCode)
	}
}

func getRawSearch(t *testing.T, url string) []byte {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d body %s", url, resp.StatusCode, raw)
	}
	return raw
}
