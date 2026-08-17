package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func TestServerSearchExposesOnlyExactRecordedFailureFingerprint(t *testing.T) {
	srv, store, _ := newTestServer(t, nil)
	sampleID := saveTestSample(t, store, "PUBLISHED")
	fingerprint := "sha256:" + strings.Repeat("ab", 32)
	if err := store.UpsertFailureCluster(t.Context(), serverstore.ClusterRow{
		Ecosystem:        "npm",
		PackageName:      "axios",
		Symbol:           "axios.post",
		Stage:            "PROJECT_COMPILE",
		ErrorFingerprint: fingerprint,
		ErrorCode:        "ERR_REQUIRE_ESM",
		ObservationCount: 7,
	}); err != nil {
		t.Fatal(err)
	}

	search := func(req domain.SearchRequest) domain.SearchResult {
		t.Helper()
		req.SchemaVersion = 2
		req.Query = "post JSON with axios"
		req.Packages = []string{"pkg:npm/axios@1.12.0"}
		req.Environment = nodeEnv("esm")
		var out domain.SearchResponse
		postJSON(t, srv.URL+"/v2/search", req, &out)
		if out.Miss || len(out.Results) == 0 {
			t.Fatalf("expected hit for request %+v", req)
		}
		return out.Results[0]
	}

	if got := search(domain.SearchRequest{ErrorFingerprint: fingerprint}); got.ExactFailureMatched {
		t.Error("fingerprint match without a selected PASS contract was promoted to an exact detour")
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, PeerID: "peer-a",
		EnvironmentHash: nodeEnv("esm").Normalize().Hash(), Environment: nodeEnv("esm"),
		Stages: map[string]string{
			"resolve": string(domain.ResultPass), "contract": string(domain.ResultPass),
		},
		ResolvedPackages: []string{"pkg:npm/axios@1.12.0"},
	}
	if err := store.SaveReceipt(t.Context(), serverstore.ReceiptRow{
		ReceiptID: "receipt-exact-failure", SampleID: sampleID, PeerID: "peer-a",
		ContractResult: "PASS", CreatedAt: testNow,
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)),
	}); err != nil {
		t.Fatal(err)
	}
	if got := search(domain.SearchRequest{ErrorFingerprint: fingerprint}); !got.ExactFailureMatched {
		t.Error("direct exact fingerprint match was not exposed")
	}
	if got := search(domain.SearchRequest{
		ErrorFingerprint:  "sha256:" + strings.Repeat("cd", 32),
		ErrorFingerprints: []string{fingerprint},
	}); !got.ExactFailureMatched {
		t.Error("stage-variant exact fingerprint match was not exposed")
	}
	if got := search(domain.SearchRequest{ErrorCode: "ERR_REQUIRE_ESM"}); got.ExactFailureMatched {
		t.Error("error-code match was promoted to an exact fingerprint match")
	}
	if got := search(domain.SearchRequest{}); got.ExactFailureMatched {
		t.Error("semantic/package hit was promoted to an exact fingerprint match")
	}
}

func TestServerFailureDetourUsesOneReceiptVariant(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("bc", 32)
	exactEnv := nodeEnv("esm")
	differentEnv := nodeEnv("esm")
	differentEnv.OS = "linux"
	differentEnv.OSVersionBucket = "24.04"

	tests := []struct {
		name            string
		resolved        string
		env             domain.EnvironmentFingerprint
		stages          map[string]string
		rowContract     string
		wantExact       bool
		wantVerified    bool
		wantPasses      int64
		wantDifferences []string
	}{
		{
			name: "same exact variant", resolved: "pkg:npm/axios@1.12.0", env: exactEnv,
			stages:      map[string]string{"resolve": "PASS", "contract": "PASS"},
			rowContract: "PASS", wantExact: true, wantVerified: true, wantPasses: 1,
		},
		{
			name: "different version and environment", resolved: "pkg:npm/axios@2.0.0", env: differentEnv,
			stages:      map[string]string{"resolve": "PASS", "contract": "PASS"},
			rowContract: "PASS", wantExact: false, wantVerified: false, wantPasses: 1,
			wantDifferences: []string{"package major version", "os windows (sample: linux)"},
		},
		{
			name:     "row fallback cannot replace signed contract stage",
			resolved: "pkg:npm/axios@1.12.0", env: exactEnv,
			stages: map[string]string{"resolve": "PASS"}, rowContract: "PASS",
			wantExact: false, wantVerified: false, wantPasses: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			manifest := testManifest()
			sampleID := "sha256:" + strings.Repeat("8b", 32)
			if err := store.SaveSample(t.Context(), serverstore.SampleRow{
				SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
				Status: "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
			}); err != nil {
				t.Fatal(err)
			}
			if err := store.UpsertFailureCluster(t.Context(), serverstore.ClusterRow{
				Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post",
				Stage: "PROJECT_COMPILE", ErrorFingerprint: fingerprint, ObservationCount: 1,
			}); err != nil {
				t.Fatal(err)
			}
			receipt := domain.VerificationReceipt{
				SchemaVersion: 2, SampleID: sampleID,
				EnvironmentHash: tc.env.Normalize().Hash(), Environment: tc.env,
				Stages: tc.stages, ResolvedPackages: []string{tc.resolved},
			}
			if err := store.SaveReceipt(t.Context(), serverstore.ReceiptRow{
				ReceiptID: "receipt-variant", SampleID: sampleID, PeerID: "peer-a",
				ContractResult: tc.rowContract, CreatedAt: testNow,
				ReceiptJSON: string(domain.MustCanonicalJSON(receipt)),
			}); err != nil {
				t.Fatal(err)
			}

			var out domain.SearchResponse
			postJSON(t, srv.URL+"/v2/search", domain.SearchRequest{
				SchemaVersion: 2, Query: "post JSON with axios",
				Packages: []string{"pkg:npm/axios@1.12.0"}, Environment: exactEnv,
				ErrorFingerprint: fingerprint,
			}, &out)
			if out.Miss || len(out.Results) == 0 {
				t.Fatalf("expected hit, got %+v", out)
			}
			got := out.Results[0]
			if got.ExactFailureMatched != tc.wantExact {
				t.Errorf("ExactFailureMatched = %v, want %v", got.ExactFailureMatched, tc.wantExact)
			}
			if got.VerifiedOffer() != tc.wantVerified {
				t.Errorf("VerifiedOffer = %v, want %v; result=%+v", got.VerifiedOffer(), tc.wantVerified, got)
			}
			if got.Evidence.ContractPasses != tc.wantPasses {
				t.Errorf("ContractPasses = %d, want %d", got.Evidence.ContractPasses, tc.wantPasses)
			}
			joined := strings.Join(got.Different, "\n")
			for _, want := range tc.wantDifferences {
				if !strings.Contains(joined, want) {
					t.Errorf("differences %q do not contain %q", joined, want)
				}
			}
		})
	}
}

func TestServerExactFailureMatchRequiresDeclaredSymbolAndNonemptyContract(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("ef", 32)
	tests := []struct {
		name                string
		candidateSym        string
		clusterPackage      string
		clusterSym          string
		contract            []string
		manifestPackages    []string
		requestPackages     []string
		omitRequestPackages bool
		receiptSchema       int
		resolvedPackages    []string
		pass                bool
		want                bool
	}{
		{
			name: "same package symbol resolved pass", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, pass: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, want: true,
		},
		{
			name: "pass resolves different package", candidateSym: "axios.get", clusterSym: "axios.get",
			contract:         []string{"returns parsed JSON"},
			manifestPackages: []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			pass:             true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, want: false,
		},
		{
			name: "explicit axios excludes lodash failure", candidateSym: "lodash.get",
			clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:         []string{"returns parsed JSON"},
			manifestPackages: []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			requestPackages:  []string{"pkg:npm/axios@1.12.0"},
			pass:             true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, want: false,
		},
		{
			name: "omitted packages allow lodash failure", candidateSym: "lodash.get",
			clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:            []string{"returns parsed JSON"},
			manifestPackages:    []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			omitRequestPackages: true,
			pass:                true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, want: true,
		},
		{
			name: "v1 pass missing resolved packages", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, pass: true, receiptSchema: 1, want: false,
		},
		{
			name:         "multi-package failure on non-worst grading package",
			candidateSym: "lodash.get", clusterPackage: "lodash", clusterSym: "lodash.get",
			contract:         []string{"returns parsed JSON"},
			manifestPackages: []string{"pkg:npm/axios@1.12.0", "pkg:npm/lodash@4.17.21"},
			requestPackages:  []string{"pkg:npm/axios@2.0.0", "pkg:npm/lodash@4.17.21"},
			pass:             true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/lodash@4.17.21"}, want: true,
		},
		{
			name: "omitted package request", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, omitRequestPackages: true,
			pass: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, want: true,
		},
		{
			name: "same package different symbol", candidateSym: "axios.post", clusterSym: "axios.get",
			contract: []string{"posts JSON"}, pass: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, want: false,
		},
		{
			name: "blank cluster symbol", candidateSym: "axios.get", clusterSym: "",
			contract: []string{"returns parsed JSON"}, pass: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, want: false,
		},
		{
			name: "missing pass", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"returns parsed JSON"}, want: false,
		},
		{
			name: "blank contract", candidateSym: "axios.get", clusterSym: "axios.get",
			contract: []string{"  "}, pass: true, receiptSchema: 2,
			resolvedPackages: []string{"pkg:npm/axios@1.12.0"}, want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := newTestServer(t, nil)
			manifest := testManifest()
			manifest.Case.Goal = "get JSON with axios"
			manifest.Case.Contract = tc.contract
			manifest.Symbols = []string{tc.candidateSym}
			manifest.Case.Symbols = []string{tc.candidateSym}
			manifestPackages := tc.manifestPackages
			if len(manifestPackages) == 0 {
				manifestPackages = []string{"pkg:npm/axios@1.12.0"}
			}
			manifest.Packages = manifestPackages
			manifest.Case.Packages = manifestPackages
			sampleID := "sha256:" + strings.Repeat("7a", 32)
			if err := store.SaveSample(t.Context(), serverstore.SampleRow{
				SampleID: sampleID, ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
				Status: "PUBLISHED", License: "MIT-0", SizeBytes: 512, CreatedAt: testNow,
			}); err != nil {
				t.Fatal(err)
			}
			if tc.pass {
				receipt := domain.VerificationReceipt{
					SchemaVersion: tc.receiptSchema, SampleID: sampleID,
					EnvironmentHash: manifest.Environment.Normalize().Hash(), Environment: manifest.Environment,
					Stages: map[string]string{
						"resolve": string(domain.ResultPass), "contract": string(domain.ResultPass),
					},
					ResolvedPackages: tc.resolvedPackages,
				}
				if err := store.SaveReceipt(t.Context(), serverstore.ReceiptRow{
					ReceiptID: "receipt-" + tc.name, SampleID: sampleID, ContractResult: "PASS", CreatedAt: testNow,
					ReceiptJSON: string(domain.MustCanonicalJSON(receipt)),
				}); err != nil {
					t.Fatal(err)
				}
			}
			clusterPackage := tc.clusterPackage
			if clusterPackage == "" {
				clusterPackage = "axios"
			}
			if err := store.UpsertFailureCluster(t.Context(), serverstore.ClusterRow{
				Ecosystem: "npm", PackageName: clusterPackage, Symbol: tc.clusterSym,
				Stage: "PROJECT_COMPILE", ErrorFingerprint: fingerprint, ObservationCount: 1,
			}); err != nil {
				t.Fatal(err)
			}
			requestPackages := tc.requestPackages
			if len(requestPackages) == 0 && !tc.omitRequestPackages {
				requestPackages = []string{"pkg:npm/axios@1.12.0"}
			}
			var out domain.SearchResponse
			postJSON(t, srv.URL+"/v2/search", domain.SearchRequest{
				SchemaVersion: 2, Query: "get JSON with axios",
				Packages: requestPackages, Environment: nodeEnv("esm"),
				ErrorFingerprint: fingerprint,
			}, &out)
			if out.Miss || len(out.Results) == 0 {
				t.Fatalf("expected candidate hit, got %+v", out)
			}
			if got := out.Results[0].ExactFailureMatched; got != tc.want {
				t.Errorf("ExactFailureMatched = %v, want %v", got, tc.want)
			}
		})
	}
}
