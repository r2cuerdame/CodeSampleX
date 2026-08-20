package httpapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func relaySnapshot(reporters int) compatibility.Snapshot {
	return compatibility.Snapshot{
		SchemaVersion: 1, PURL: "pkg:npm/lonely@1.0.0", Symbol: "",
		Rows: []compatibility.SnapshotRow{{
			ContextLabel: "node",
			EnvBucket: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "x64",
				Runtime: "node", RuntimeVersion: "20.11.0", PackageManager: "npm",
			},
			ByStage: map[string]compatibility.StageCount{
				"PROJECT_COMPILE": {Pass: 297, Fail: 15},
			},
			UniquePeerBuckets: reporters,
			LastSeen:          "2026-08-19T00:00:00Z",
		}},
		Failures: []compatibility.FailureSummary{{
			Stage: "PROJECT_COMPILE", ErrorCode: "ERR_REQUIRE_ESM",
			Fingerprint: "sha256:aaa", Count: 15,
			EnvSummary: map[string]string{"os": "windows"},
		}},
	}
}

// A miss with nothing to say is the cold-start trap: the network holds
// hundreds of recorded runs for a coordinate and answers "nothing known"
// because no sample was ever written for it. The grade stays honest; the
// empty hand does not.
func TestMissRelaysWhatWasRecorded(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	js, err := json.Marshal(relaySnapshot(184))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(context.Background(), "pkg:npm/lonely@1.0.0", "", string(js)); err != nil {
		t.Fatal(err)
	}

	var got domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v2/search", map[string]any{
		"schemaVersion": 1, "goal": "use lonely",
		"packages":    []string{"pkg:npm/lonely@1.0.0"},
		"environment": map[string]any{"schemaVersion": 1, "ecosystem": "npm", "os": "windows"},
	}, nil)
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if !got.Miss {
		t.Fatal("expected a miss")
	}
	if got.Observed == nil {
		t.Fatal("a miss returned nothing, though the network holds 312 recorded runs")
	}
	if got.Observed.Basis != domain.ObservedBasis {
		t.Errorf("basis = %q, want %q", got.Observed.Basis, domain.ObservedBasis)
	}
	if got.Observed.Note == "" {
		t.Error("relayed observations travelled without their disclaimer")
	}
	if len(got.Observed.Cells) != 1 {
		t.Fatalf("cells = %+v, want one", got.Observed.Cells)
	}
	cell := got.Observed.Cells[0]
	if cell.Pass != 297 || cell.Fail != 15 {
		t.Errorf("cell = %d/%d, want 297 pass and 15 fail", cell.Pass, cell.Fail)
	}
	if cell.Reporters != 184 {
		t.Errorf("reporters = %d, want the recorded peak 184", cell.Reporters)
	}
	// The relayed version is the BUCKETED one. A full patch version against a
	// stable anonymous id narrows the population more than it informs, and
	// the bucket is the dimension the rest of the site already reasons in.
	if cell.Environment.OS != "windows" || cell.Environment.RuntimeVersion != "20.11" {
		t.Errorf("environment = %+v", cell.Environment)
	}
	if len(got.Observed.Errors) != 1 || got.Observed.Errors[0].ErrorCode != "ERR_REQUIRE_ESM" {
		t.Errorf("errors = %+v, want the recorded error code", got.Observed.Errors)
	}
}

// A single reporting machine is still relayed — withholding it would be a
// judgement about sufficiency, and this payload makes none. What must never
// happen is relaying it WITHOUT saying how thin it is, so the count travels
// on every cell and the rendered text leads with it.
func TestASingleReporterIsRelayedAndSaysSo(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	js, err := json.Marshal(relaySnapshot(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(context.Background(), "pkg:npm/lonely@1.0.0", "", string(js)); err != nil {
		t.Fatal(err)
	}
	var got domain.SearchResponse
	resp := postJSON(t, srv.URL+"/v2/search", map[string]any{
		"schemaVersion": 1, "goal": "use lonely",
		"packages":    []string{"pkg:npm/lonely@1.0.0"},
		"environment": map[string]any{"schemaVersion": 1, "ecosystem": "npm", "os": "windows"},
	}, nil)
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Observed == nil || len(got.Observed.Cells) == 0 {
		t.Fatal("a single reporter's recorded runs were withheld")
	}
	if got.Observed.Cells[0].Reporters != 1 {
		t.Errorf("reporters = %d, want the honest 1", got.Observed.Cells[0].Reporters)
	}
}

// v1 is a frozen byte shape. Doing the store work and discarding it would
// also cost a read on the network's most common outcome.
func TestV1MissCarriesNoRelay(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	js, _ := json.Marshal(relaySnapshot(184))
	if err := store.PutSnapshot(context.Background(), "pkg:npm/lonely@1.0.0", "", string(js)); err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, srv.URL+"/v1/search", map[string]any{
		"schemaVersion": 1, "goal": "use lonely",
		"packages":    []string{"pkg:npm/lonely@1.0.0"},
		"environment": map[string]any{"schemaVersion": 1, "ecosystem": "npm", "os": "windows"},
	}, nil)
	defer resp.Body.Close()
	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["observed"]; present {
		t.Error("v1 response gained an observed key")
	}
}
