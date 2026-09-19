package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/fixclaims"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const fixWriterToken = "csx_author_v1_YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJiYmI"

func fixPost(t *testing.T, srv, token, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv+path, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func fixGet(t *testing.T, srv, token, path string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

const fixGoodCandidate = `{"schemaVersion":1,"ecosystem":"npm","name":"foo","claimedBadVersion":"2.4.0","claimedFixedVersion":"2.4.1","claim":"Fixed crash when parse() receives an empty buffer","sourceUrl":"https://github.com/acme/foo/releases/tag/v2.4.1","sourceType":"release_note","symbols":["parse"],"confidence":"high"}`

// fixSampleWithReceipt stores a sample pinning one release of npm/foo and a
// receipt for it with the given contract result.
func fixSampleWithReceipt(t *testing.T, store *serverstore.Fake, version, result string) (sampleID, receiptID string) {
	t.Helper()
	sampleID = fmt.Sprintf("sha256:%064x", []byte(strings.ReplaceAll(version, ".", "")+result))
	receiptID = "receipt-" + version + "-" + result
	manifest := fmt.Sprintf(`{"schemaVersion":1,"case":{"kind":"FIX","goal":"parse empty buffer","packages":["pkg:npm/foo@%s"],"contract":["node run.js"]},"packages":["pkg:npm/foo@%s"],"symbols":["parse"],"subject":"pkg:npm/foo@%s","environment":{"os":"linux"},"license":"MIT-0","contractCommand":["node","run.js"],"verifierAdapter":"npm"}`, version, version, version)
	if err := store.SaveSample(t.Context(), serverstore.SampleRow{SampleID: sampleID, ManifestJSON: manifest, Status: "PUBLISHED", CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveReceipt(t.Context(), serverstore.ReceiptRow{ReceiptID: receiptID, SampleID: sampleID, PeerID: "farm-1", EnvHash: "env", ReceiptJSON: "{}", ContractResult: result, CreatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	return sampleID, receiptID
}

func TestFixCandidatesIngestValidatesAndNeverAcceptsAStatus(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	authoringSession(t, store, fixWriterToken, "collector-a", testNow)

	if code, _ := fixPost(t, srv.URL, "", "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+fixGoodCandidate+`]}`); code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", code)
	}
	docsOnly := strings.Replace(fixGoodCandidate, `"claim":"Fixed crash when parse() receives an empty buffer"`, `"claim":"Fixed a typo in the README documentation"`, 1)
	docsOnly = strings.Replace(docsOnly, `"symbols":["parse"],`, ``, 1)
	code, body := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+fixGoodCandidate+`,`+docsOnly+`]}`)
	if code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, body)
	}
	accepted := body["accepted"].([]any)
	rejected := body["rejected"].([]any)
	if len(accepted) != 1 || len(rejected) != 1 {
		t.Fatalf("accepted=%v rejected=%v", accepted, rejected)
	}
	first := accepted[0].(map[string]any)
	if first["status"] != string(fixclaims.StatusClaimedFix) || first["duplicate"] != false || first["id"].(float64) < 1 {
		t.Fatalf("accepted record: %v", first)
	}
	rej := rejected[0].(map[string]any)
	if rej["index"].(float64) != 1 || !strings.Contains(fmt.Sprint(rej["rejections"]), fixclaims.RejectNonExecutable) {
		t.Fatalf("rejection: %v", rej)
	}

	// A producer cannot smuggle a status: the field is unknown to the schema.
	withStatus := strings.TrimSuffix(fixGoodCandidate, "}") + `,"status":"VERIFIED_FIX"}`
	if code, body := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+withStatus+`]}`); code != http.StatusBadRequest {
		t.Fatalf("status field accepted: %d %v", code, body)
	}
	// Resubmitting is a duplicate, and the door counters saw both rounds.
	code, body = fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+fixGoodCandidate+`]}`)
	if code != http.StatusOK || body["accepted"].([]any)[0].(map[string]any)["duplicate"] != true {
		t.Fatalf("duplicate: %d %v", code, body)
	}
	code, metrics := fixGet(t, srv.URL, fixWriterToken, "/v1/fix-claims/metrics")
	if code != http.StatusOK || metrics["ingested"].(float64) != 3 || metrics["rejected"].(float64) != 1 || metrics["accepted"].(float64) != 1 {
		t.Fatalf("metrics: %d %v", code, metrics)
	}
	if code, _ := fixGet(t, srv.URL, "", "/v1/fix-claims/metrics"); code != http.StatusUnauthorized {
		t.Fatalf("metrics open to the public: %d", code)
	}
}

func TestFixWorkRunsNeedReceiptsAndOnlyReceiptsMoveTheStatus(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	authoringSession(t, store, fixWriterToken, "farm-a", testNow)
	for _, v := range []string{"2.3.9", "2.4.0", "2.4.1", "2.4.2"} {
		if err := store.UpsertPackage(t.Context(), serverstore.PackageRow{PURL: "pkg:npm/foo@" + v, Ecosystem: "npm", Name: "foo", Version: v, Publicness: "PUBLIC"}); err != nil {
			t.Fatal(err)
		}
	}
	code, body := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+fixGoodCandidate+`]}`)
	if code != http.StatusOK {
		t.Fatalf("ingest: %d %v", code, body)
	}
	id := int64(body["accepted"].([]any)[0].(map[string]any)["id"].(float64))
	path := fmt.Sprintf("/v1/fix-claims/%d", id)

	// Nothing has run: the public record is CLAIMED_FIX and not verified.
	code, rec := fixGet(t, srv.URL, "", "/v1/fix-claims?purl=pkg:npm/foo@2.4.1")
	if code != http.StatusOK {
		t.Fatalf("list: %d %v", code, rec)
	}
	item := rec["items"].([]any)[0].(map[string]any)
	if item["status"] != string(fixclaims.StatusClaimedFix) || item["verified"] != false || len(item["evidence"].([]any)) != 0 {
		t.Fatalf("claimed record presented as more than a claim: %v", item)
	}
	if _, ok := rec["semantics"].(map[string]any)[string(fixclaims.StatusClaimedFix)]; !ok {
		t.Fatal("semantics missing")
	}

	// Take the work: the first turn asks for the pair, in the worker's OS.
	code, work := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/work/next", `{"schemaVersion":1,"verifierOS":["linux"]}`)
	if code != http.StatusOK || work["status"] != string(serverstore.FixClaimAssigned) {
		t.Fatalf("work: %d %v", code, work)
	}
	w := work["work"].(map[string]any)
	if int64(w["id"].(float64)) != id {
		t.Fatalf("wrong candidate: %v", w)
	}
	probes := w["probes"].([]any)
	if len(probes) != 2 || probes[0].(map[string]any)["version"] != "2.4.0" || probes[1].(map[string]any)["version"] != "2.4.1" {
		t.Fatalf("probes: %v", probes)
	}
	if w["reproducer"].(map[string]any)["source"] != string(fixclaims.ReproducerGenerated) {
		t.Fatalf("reproducer: %v", w["reproducer"])
	}

	badSample, badReceipt := fixSampleWithReceipt(t, store, "2.4.0", "FAIL")
	fixedSample, fixedReceipt := fixSampleWithReceipt(t, store, "2.4.1", "PASS")
	fp := strings.Repeat("c", 64)
	runsBody := func(badVerdict, badRc, fixedVerdict, fixedRc string) string {
		return fmt.Sprintf(`{"schemaVersion":1,"runs":[
			{"version":"2.4.0","environment":{"os":"linux"},"verdict":"%s","failureFingerprint":"%s","receiptId":"%s","sampleId":"%s","farmSeconds":40},
			{"version":"2.4.1","environment":{"os":"linux"},"verdict":"%s","receiptId":"%s","sampleId":"%s","farmSeconds":35}]}`,
			badVerdict, fp, badRc, badSample, fixedVerdict, fixedRc, fixedSample)
	}
	// A run whose receipt does not exist is refused.
	if code, body := fixPost(t, srv.URL, fixWriterToken, path+"/runs", runsBody("FAIL", "no-such-receipt", "PASS", fixedReceipt)); code != http.StatusBadRequest {
		t.Fatalf("unreceipted run accepted: %d %v", code, body)
	}
	// A run whose verdict contradicts its receipt is refused.
	if code, body := fixPost(t, srv.URL, fixWriterToken, path+"/runs", runsBody("PASS", badReceipt, "PASS", fixedReceipt)); code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "recorded contract FAIL") {
		t.Fatalf("contradicting run accepted: %d %v", code, body)
	}
	// A run filed under a release the sample does not pin is refused.
	swapped := strings.Replace(runsBody("FAIL", badReceipt, "PASS", fixedReceipt), `"version":"2.4.1"`, `"version":"2.4.2"`, 1)
	if code, body := fixPost(t, srv.URL, fixWriterToken, path+"/runs", swapped); code != http.StatusBadRequest || !strings.Contains(body["error"].(string), "does not pin") {
		t.Fatalf("mispinned run accepted: %d %v", code, body)
	}
	if code, _ := fixPost(t, srv.URL, fixWriterToken, path+"/reproducer", `{"schemaVersion":1,"source":"GENERATED","sampleId":"`+badSample+`"}`); code != http.StatusOK {
		t.Fatalf("reproducer: %d", code)
	}
	// The receipted pair is admitted and the record becomes VERIFIED_FIX.
	code, body = fixPost(t, srv.URL, fixWriterToken, path+"/runs", runsBody("FAIL", badReceipt, "PASS", fixedReceipt))
	if code != http.StatusOK {
		t.Fatalf("runs: %d %v", code, body)
	}
	record := body["record"].(map[string]any)
	if record["status"] != string(fixclaims.StatusVerifiedFix) || record["verified"] != true || record["badVersion"] != "2.4.0" || record["goodVersion"] != "2.4.1" || record["failureFingerprint"] != fp {
		t.Fatalf("record: %v", record)
	}
	if ev := record["evidence"].([]any); len(ev) != 2 || ev[0] != badReceipt || ev[1] != fixedReceipt {
		t.Fatalf("evidence: %v", record["evidence"])
	}
	if up := record["upstream"].(map[string]any); up["url"] != "https://github.com/acme/foo/releases/tag/v2.4.1" || up["type"] != "release_note" {
		t.Fatalf("provenance lost: %v", up)
	}
	next := body["nextProbes"].([]any)
	if len(next) != 2 || next[0].(map[string]any)["version"] != "2.3.9" || next[1].(map[string]any)["version"] != "2.4.2" {
		t.Fatalf("expansion: %v", next)
	}
	// The lease was released with the runs; a second report has no lease.
	if code, _ := fixPost(t, srv.URL, fixWriterToken, path+"/runs", runsBody("FAIL", badReceipt, "PASS", fixedReceipt)); code != http.StatusConflict {
		t.Fatalf("run without lease: %d", code)
	}
	// The public read now answers the product question.
	code, rec = fixGet(t, srv.URL, "", "/v1/fix-claims?ecosystem=npm&name=foo&version=2.4.1&status=VERIFIED_FIX")
	if code != http.StatusOK || len(rec["items"].([]any)) != 1 {
		t.Fatalf("query: %d %v", code, rec)
	}
	code, one := fixGet(t, srv.URL, "", path)
	if code != http.StatusOK || one["status"] != string(fixclaims.StatusVerifiedFix) || len(one["runs"].([]any)) != 2 {
		t.Fatalf("get: %d %v", code, one)
	}
	if code, _ := fixGet(t, srv.URL, "", "/v1/fix-claims"); code != http.StatusBadRequest {
		t.Fatalf("unbounded list allowed: %d", code)
	}
	// Metrics count the verified fix and the Farm seconds it cost.
	code, metrics := fixGet(t, srv.URL, fixWriterToken, "/v1/fix-claims/metrics")
	if code != http.StatusOK || metrics["fixConfirmed"].(float64) != 1 || metrics["farmSeconds"].(float64) != 75 || metrics["farmSecondsPerVerifiedFix"].(float64) != 75 {
		t.Fatalf("metrics: %v", metrics)
	}
}

func TestFixWorkBudgetIsIndependentAndOutcomesHandBack(t *testing.T) {
	srv, store, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.FixClaim = serverstore.FixClaimLimits{MaxLeases: 1, MaxAttempts: 1, MaxRuns: 12}
	})
	const tokenB = "csx_author_v1_Y2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2NjY2M"
	authoringSession(t, store, fixWriterToken, "farm-a", testNow)
	authoringSession(t, store, tokenB, "farm-b", testNow)
	second := strings.Replace(fixGoodCandidate, `"name":"foo"`, `"name":"bar"`, 1)
	if code, _ := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/candidates", `{"schemaVersion":1,"candidates":[`+fixGoodCandidate+`,`+second+`]}`); code != http.StatusOK {
		t.Fatal(code)
	}
	code, work := fixPost(t, srv.URL, fixWriterToken, "/v1/fix-claims/work/next", `{"schemaVersion":1}`)
	if code != http.StatusOK || work["status"] != string(serverstore.FixClaimAssigned) {
		t.Fatalf("%d %v", code, work)
	}
	id := int64(work["work"].(map[string]any)["id"].(float64))
	if code, other := fixPost(t, srv.URL, tokenB, "/v1/fix-claims/work/next", `{"schemaVersion":1}`); code != http.StatusOK || other["status"] != string(serverstore.FixClaimBudgetExhausted) {
		t.Fatalf("second lease inside a ceiling of one: %d %v", code, other)
	}
	// The wrong session cannot hand it back; the right one can, and a
	// signal-less candidate at the attempt cap is closed, not deleted.
	if code, _ := fixPost(t, srv.URL, tokenB, fmt.Sprintf("/v1/fix-claims/%d/outcome", id), `{"schemaVersion":1,"outcome":"NO_OUTPUT"}`); code != http.StatusConflict {
		t.Fatalf("other session released: %d", code)
	}
	if code, _ := fixPost(t, srv.URL, fixWriterToken, fmt.Sprintf("/v1/fix-claims/%d/outcome", id), `{"schemaVersion":1,"outcome":"COMPLETE"}`); code != http.StatusBadRequest {
		t.Fatalf("server bookkeeping accepted from a client: %d", code)
	}
	code, released := fixPost(t, srv.URL, fixWriterToken, fmt.Sprintf("/v1/fix-claims/%d/outcome", id), `{"schemaVersion":1,"outcome":"no_reproducer","detail":"nothing builds at 2.4.0"}`)
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, released)
	}
	rec := released["record"].(map[string]any)
	if rec["closed"] != true || rec["closedReason"] != serverstore.FixClosedAttemptCap || rec["status"] != string(fixclaims.StatusClaimedFix) {
		t.Fatalf("attempt cap: %v", rec)
	}
	code, one := fixGet(t, srv.URL, "", fmt.Sprintf("/v1/fix-claims/%d", id))
	if code != http.StatusOK || one["verified"] != false {
		t.Fatalf("closed record: %d %v", code, one)
	}
	// The other candidate is still offered; a disabled lane offers nothing.
	if code, next := fixPost(t, srv.URL, tokenB, "/v1/fix-claims/work/next", `{"schemaVersion":1}`); code != http.StatusOK || next["status"] != string(serverstore.FixClaimAssigned) {
		t.Fatalf("%d %v", code, next)
	}
	off, offStore, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.FixClaim = serverstore.FixClaimLimits{MaxLeases: 0, MaxAttempts: 3, MaxRuns: 12}
	})
	authoringSession(t, offStore, fixWriterToken, "farm-a", testNow)
	if code, body := fixPost(t, off.URL, fixWriterToken, "/v1/fix-claims/work/next", `{"schemaVersion":1}`); code != http.StatusOK || body["status"] != string(serverstore.FixClaimBudgetExhausted) {
		t.Fatalf("disabled lane: %d %v", code, body)
	}
}

func TestFixClaimLimitsFromEnv(t *testing.T) {
	env := map[string]string{"CSX_FIX_WORK_MAX_LEASES": "0", "CSX_FIX_MAX_ATTEMPTS": "x", "CSX_FIX_MAX_RUNS": "20"}
	lim := serverstore.FixClaimLimitsFromEnv(func(k string) string { return env[k] })
	if lim.MaxLeases != 0 || lim.MaxAttempts != 3 || lim.MaxRuns != 20 {
		t.Fatalf("%+v", lim)
	}
}
