package web

import (
	"context"
	"strings"
)

// fakeStore is the full in-memory Store implementation used by the web
// tests. The integrator adapts the real serverstore to the same
// interface; nothing here touches a database.
type fakeStore struct {
	coverage     []CoverageRow
	wanted       []WantedRow
	statsJSON    string
	statsOK      bool
	snapshots    map[string]string // purl+"\x00"+symbol → snapshot JSON
	versions     map[string][]string
	symbolSpread map[string]int
	symbols      map[string][]string // eco+"|"+name+"|"+version → families
	samples      map[string]SampleMeta
	receipts     map[string][]string
	seeders      map[string][]SampleListItem
	packages     []PackageHit
	clusters     map[string][]string // eco+"|"+name → cluster JSON
	// sampleList is every published sample, newest first (sitemap +
	// package pages); samplePackages is the purl list of each one.
	dependencies   []DependencyEdge
	sampleList     []SampleListItem
	samplePackages map[string][]string
	packageCodeErr error
	derived        []DerivedFinding
}

func snapKey(purl, symbol string) string { return purl + "\x00" + symbol }

func (f *fakeStore) LatestStatsJSON(_ context.Context) (string, bool) {
	return f.statsJSON, f.statsOK
}

func (f *fakeStore) SnapshotJSON(_ context.Context, purl, symbol string) (string, bool) {
	s, ok := f.snapshots[snapKey(purl, symbol)]
	return s, ok
}

func (f *fakeStore) PackageVersions(_ context.Context, ecosystem, name string) ([]string, error) {
	return f.versions[ecosystem+"|"+name], nil
}

func (f *fakeStore) SymbolPackageSpread(_ context.Context, _ string, symbols []string) (map[string]int, error) {
	if f.symbolSpread == nil {
		return nil, nil
	}
	out := map[string]int{}
	for _, sym := range symbols {
		if n, ok := f.symbolSpread[sym]; ok {
			out[sym] = n
		}
	}
	return out, nil
}

func (f *fakeStore) PackageSymbols(_ context.Context, ecosystem, name, version string) ([]string, error) {
	return f.symbols[ecosystem+"|"+name+"|"+version], nil
}

func (f *fakeStore) SampleMeta(_ context.Context, id string) (SampleMeta, bool) {
	m, ok := f.samples[id]
	return m, ok
}

func (f *fakeStore) SampleManifest(_ context.Context, id string) (string, bool) {
	m, ok := f.samples[id]
	return m.ManifestJSON, ok
}

func (f *fakeStore) SampleReceipts(_ context.Context, id string) ([]string, error) {
	return f.receipts[id], nil
}

func (f *fakeStore) SeederSamples(_ context.Context, login string) ([]SampleListItem, error) {
	return f.seeders[login], nil
}

func (f *fakeStore) ListSamples(_ context.Context, limit int) ([]SampleListItem, error) {
	out := make([]SampleListItem, 0, len(f.sampleList))
	for _, it := range f.sampleList {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, it)
	}
	return out, nil
}

// derivedFindings lets a test hand the findings page machine-derived rows.
func (f *fakeStore) DerivedFindings(_ context.Context, limit int) ([]DerivedFinding, error) {
	if limit < len(f.derived) {
		return f.derived[:limit], nil
	}
	return f.derived, nil
}

func (f *fakeStore) PackageSamples(_ context.Context, ecosystem, name string, limit int) ([]SampleListItem, error) {
	prefix := "pkg:" + ecosystem + "/" + name + "@"
	var out []SampleListItem
	for _, it := range f.sampleList {
		if limit > 0 && len(out) >= limit {
			break
		}
		for _, p := range f.samplePackages[it.SampleID] {
			if !strings.HasPrefix(p, prefix) {
				continue
			}
			// The real adapter reads the version out of the manifest purl;
			// the fake derives it the same way so version-scoped pages are
			// exercised rather than silently empty.
			if it.Version == "" {
				it.Version = strings.TrimPrefix(p, prefix)
			}
			out = append(out, it)
			break
		}
	}
	return out, nil
}

func (f *fakeStore) PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]PackageCodeCount, error) {
	if f.packageCodeErr != nil {
		return nil, f.packageCodeErr
	}
	items, err := f.PackageSamples(ctx, ecosystem, name, 0)
	if err != nil {
		return nil, err
	}
	counts := map[[2]string]int64{}
	for _, item := range items {
		if item.Version == "" {
			continue
		}
		counts[[2]string{item.Version, ""}]++
		seen := map[string]bool{}
		for _, symbol := range item.Symbols {
			if symbol != "" && !seen[symbol] {
				counts[[2]string{item.Version, symbol}]++
				seen[symbol] = true
			}
		}
	}
	out := make([]PackageCodeCount, 0, len(counts))
	for key, count := range counts {
		out = append(out, PackageCodeCount{Version: key[0], Symbol: key[1], Samples: count})
	}
	return out, nil
}

func (f *fakeStore) SearchPackages(_ context.Context, q string, limit int) ([]PackageHit, error) {
	var hits []PackageHit
	for _, p := range f.packages {
		if strings.Contains(p.Name, q) {
			hits = append(hits, p)
		}
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func (f *fakeStore) HotPackages(_ context.Context, limit int) ([]PackageHit, error) {
	if len(f.packages) > limit {
		return f.packages[:limit], nil
	}
	return f.packages, nil
}

func (f *fakeStore) RecordPackages(_ context.Context, filter RecordFilter, offset, limit int) ([]PackageHit, int, error) {
	var all []PackageHit
	query := ParseRecordQuery(filter.Query)
	for _, p := range f.packages {
		queryMatch, _, _ := query.MatchPackage(p.Name)
		if queryMatch &&
			(filter.Ecosystem == "" || p.Ecosystem == filter.Ecosystem) &&
			(filter.OS == "" || containsString(p.OperatingSystems, filter.OS)) &&
			(filter.Runtime == "" || containsString(p.Runtimes, filter.Runtime)) &&
			(filter.Basis == "" || containsString(p.EvidenceBases, filter.Basis)) {
			all = append(all, p)
		}
	}
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	all = all[offset:]
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, total, nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (f *fakeStore) FailureClusters(_ context.Context, ecosystem, name string) ([]string, int, error) {
	rows := f.clusters[ecosystem+"|"+name]
	return rows, len(rows), nil
}

// newFakeStore builds the shared fixture: axios with a context-first
// snapshot (HIGH / ELEVATED FAILURE / UNKNOWN no-evidence rows), a golang
// multi-segment package, one sample with a receipt, and one seeder.
func newFakeStore() *fakeStore {
	f := &fakeStore{
		statsJSON: `{"schemaVersion":1,"peers":12,"packages":340,"symbols":1200,` +
			`"evidence":45213,"verifiedSamples":31,"postHitSuccessRate":0.87,` +
			// A rate needs the count it came from: the fixture describes a
			// network with a real success rate, so it has to say how many
			// builds produced it, or the tiles are correctly hidden.
			`"postHitBuildsReported":64,` +
			`"estimatedReasoningAvoided":1204,"estimated":true,"generatedAt":"2026-08-13T00:00:00Z"}`,
		statsOK:   true,
		snapshots: map[string]string{},
		versions: map[string][]string{
			"npm|axios":             {"1.12.0", "1.11.0"},
			"golang|github.com/a/b": {"v1.2.0"},
		},
		symbols: map[string][]string{
			"npm|axios|1.12.0":             {"axios.post", "axios.get"},
			"golang|github.com/a/b|v1.2.0": {"pkg.Func"},
		},
		samples:  map[string]SampleMeta{},
		receipts: map[string][]string{},
		seeders:  map[string][]SampleListItem{},
		packages: []PackageHit{
			{Ecosystem: "npm", Name: "axios", LatestVersion: "1.12.0", Symbols: 2, EvidenceCount: 45000,
				OperatingSystems: []string{"linux", "windows"}, Runtimes: []string{"node"}, EvidenceBases: []string{"observed", "verified"}},
			{Ecosystem: "golang", Name: "github.com/a/b", LatestVersion: "v1.2.0", Symbols: 1, EvidenceCount: 12,
				OperatingSystems: []string{"linux"}, Runtimes: []string{"go"}, EvidenceBases: []string{"observed"}},
		},
		clusters: map[string][]string{},
	}

	symbolSnapshot := `{
	  "schemaVersion": 1,
	  "purl": "pkg:npm/axios@1.12.0",
	  "symbol": "axios.post",
	  "generatedAt": "2026-08-13T00:00:00Z",
	  "rows": [
	    {
	      "contextLabel": "node 22",
	      "envLabel": "TS 5.9 · pnpm · windows",
	      "confidence": "HIGH",
	      "passRate": 0.96,
	      "uniquePeerBuckets": 9,
	      "lastSeen": "2026-08-12T10:00:00Z",
	      "byStage": {
	        "PROJECT_COMPILE": {"pass": 100, "fail": 4},
	        "CONTRACT": {"pass": 6, "fail": 1}
	      }
	    },
	    {
	      "contextLabel": "safari 19",
	      "envLabel": "macos",
	      "confidence": "ELEVATED_FAILURE",
	      "passRate": 0.42,
	      "uniquePeerBuckets": 4,
	      "lastSeen": "2026-08-11T10:00:00Z",
	      "byStage": {"PROJECT_LOAD": {"pass": 5, "fail": 7}}
	    },
	    {
	      "contextLabel": "android-webview 140",
	      "envLabel": "",
	      "confidence": "",
	      "passRate": 0,
	      "uniquePeerBuckets": 0,
	      "lastSeen": "",
	      "byStage": {}
	    }
	  ],
	  "failures": [
	    {
	      "stage": "PROJECT_PROCESS",
	      "errorCode": "ERR_REQUIRE_ESM",
	      "fingerprint": "sha256:aabbcc",
	      "count": 7,
	      "envSummary": {"moduleSystem": "esm", "runtime": "node@18"},
	      "hypotheses": [
	        {"domain": "CONFIGURATION", "confidence": 0.72},
	        {"domain": "UNKNOWN", "confidence": 0.28}
	      ],
	      "regressionCandidate": true,
	      "versions": ["1.11.0", "1.12.0"]
	    }
	  ]
	}`
	f.snapshots[snapKey("pkg:npm/axios@1.12.0", "axios.post")] = symbolSnapshot
	f.snapshots[snapKey("pkg:golang/github.com/a/b@v1.2.0", "pkg.Func")] = `{
	  "schemaVersion": 1,
	  "purl": "pkg:golang/github.com/a/b@v1.2.0",
	  "symbol": "pkg.Func",
	  "rows": [
	    {"contextLabel": "go 1.26", "envLabel": "linux", "confidence": "MEDIUM",
	     "passRate": 0.9, "uniquePeerBuckets": 2, "lastSeen": "2026-08-10T00:00:00Z",
	     "byStage": {"PROJECT_COMPILE": {"pass": 9, "fail": 1}}}
	  ],
	  "failures": []
	}`

	f.clusters["npm|axios"] = []string{`{
	  "symbol": "axios.post",
	  "stage": "PROJECT_PROCESS",
	  "errorCode": "ERR_REQUIRE_ESM",
	  "fingerprint": "sha256:aabbcc",
	  "observationCount": 7,
	  "envSummary": {"moduleSystem": "esm"},
	  "hypotheses": [{"domain": "CONFIGURATION", "confidence": 0.72}],
	  "regressionCandidate": true,
	  "versions": ["1.11.0", "1.12.0"]
	}`, `{
	  "stage": "PROJECT_TEST",
	  "errorCode": "",
	  "fingerprint": "sha256:ddeeff",
	  "observationCount": 3,
	  "envSummary": {"os": "windows", "runtime": "node@22.16"},
	  "hypotheses": [],
	  "regressionCandidate": false,
	  "versions": ["1.12.0"]
	}`}

	manifest := `{
	  "schemaVersion": 1,
	  "case": {
	    "schemaVersion": 1,
	    "caseId": "case:sha256:9999",
	    "kind": "HOW",
	    "goal": "POST JSON with axios and retries",
	    "packages": ["pkg:npm/axios@1.12.0"],
	    "contract": ["responds 200", "retries on ECONNRESET"]
	  },
	  "packages": ["pkg:npm/axios@1.12.0"],
	  "symbols": ["axios.post"],
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64",
	    "runtime":"node","runtimeVersion":"22.18","executionContext":"node"},
	  "license": "MIT-0",
	  "contractCommand": ["node", "test/contract.mjs"],
	  "verifierAdapter": "node-typescript@1"
	}`
	f.samples["sha256:d1e2f3"] = SampleMeta{
		SampleID:     "sha256:d1e2f3",
		Status:       "CROSS_PASS",
		License:      "MIT-0",
		OriginSeeder: "alice",
		CreatedAt:    "2026-08-01T00:00:00Z",
		ManifestJSON: manifest,
		Files:        []string{"csx.json", "src/index.mjs", "test/contract.mjs"},
	}
	f.receipts["sha256:d1e2f3"] = []string{`{
	  "schemaVersion": 1,
	  "sampleId": "sha256:d1e2f3",
	  "caseId": "case:sha256:9999",
	  "environmentHash": "sha256:eeee",
	  "environment": {"schemaVersion":1,"ecosystem":"npm","os":"linux","arch":"amd64",
	    "runtime":"node","runtimeVersion":"22.18","executionContext":"node"},
	  "stages": {"resolve":"PASS","compile":"PASS","contract":"PASS"},
	  "verifierAdapter": "node-typescript@1",
	  "sandboxCapability": "CONTAINER_RUN",
	  "logsDigest": "sha256:ffff",
	  "createdAt": "2026-08-02T00:00:00Z",
	  "peerId": "ed25519:0011223344556677",
	  "peerPubkey": "cHVi",
	  "peerSignature": "c2ln"
	}`}
	item := SampleListItem{
		SampleID: "sha256:d1e2f3", Goal: "POST JSON with axios and retries",
		Status: "CROSS_PASS", License: "MIT-0", Context: "node 22.18",
		CreatedAt: "2026-08-01",
		Version:   "1.12.0", Symbols: []string{"axios.post"}, Kind: "HOW",
	}
	f.seeders["alice"] = []SampleListItem{item}
	f.sampleList = []SampleListItem{item}
	f.samplePackages = map[string][]string{
		"sha256:d1e2f3": {"pkg:npm/axios@1.12.0"},
	}
	return f
}

func (f *fakeStore) Coverage(context.Context) ([]CoverageRow, error) { return f.coverage, nil }

func (f *fakeStore) Dependencies(context.Context, string, string) ([]DependencyEdge, error) {
	return f.dependencies, nil
}
