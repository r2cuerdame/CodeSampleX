package apidemand

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUserAgentGrammarRoundTrips(t *testing.T) {
	cases := []struct {
		version, surface, protocol string
		want                       Client
		ua                         string
	}{
		{"v0.1.195", "cli", "", Client{Kind: ClientCLI, Version: "v0.1.195"}, "csx/v0.1.195 (cli)"},
		{"v0.1.195", "mcp", "2025-06-18", Client{Kind: ClientMCP, Version: "v0.1.195", Protocol: "2025-06-18"}, "csx/v0.1.195 (mcp; protocol=2025-06-18)"},
		{"dev (git)", "daemon", "", Client{Kind: ClientDaemon, Version: "devgit"}, "csx/devgit (daemon)"},
		{"", "farm", "", Client{Kind: ClientCSX, Version: "unknown"}, "csx/unknown (farm)"},
	}
	for _, tc := range cases {
		ua := UserAgent(tc.version, tc.surface, tc.protocol)
		if ua != tc.ua {
			t.Fatalf("UserAgent(%q,%q,%q)=%q want %q", tc.version, tc.surface, tc.protocol, ua, tc.ua)
		}
		if got := ParseUserAgent(ua); got != tc.want {
			t.Fatalf("ParseUserAgent(%q)=%+v want %+v", ua, got, tc.want)
		}
	}
}

func TestParseUserAgentNeverKeepsForeignText(t *testing.T) {
	for _, ua := range []string{"Mozilla/5.0 (Windows NT 10.0) Chrome/120", "curl/8.4.0", "csx/../etc (cli)", "csx/v1.0.0 (CLI)", "csx/v1.0.0 (cli; protocol=<script>)"} {
		got := ParseUserAgent(ua)
		if got.Kind != ClientOther || got.Version != "" || got.Protocol != "" {
			t.Fatalf("ParseUserAgent(%q)=%+v: foreign agent must collapse to other", ua, got)
		}
	}
	if got := ParseUserAgent(""); got.Kind != ClientNone {
		t.Fatalf("empty user agent = %+v", got)
	}
}

func TestReleaseOrdering(t *testing.T) {
	old, ok := ParseRelease("v0.1.194")
	if !ok {
		t.Fatal("v0.1.194 must parse")
	}
	current, _ := ParseRelease("0.1.195")
	pre, ok := ParseRelease("v0.2.0-rc.1")
	if !ok || pre.Minor != 2 {
		t.Fatalf("prerelease parse = %+v %v", pre, ok)
	}
	if !old.Less(current) || current.Less(old) || !current.Less(pre) {
		t.Fatal("release ordering is wrong")
	}
	for _, raw := range []string{"dev", "devgit", "unknown", "v1", "1.2", "v1.2.3.4.5.6.7.8.9.10.11.12.13.14.15.16.17.18.19.20.21.22.23.24.25.26.27.28.29.30"} {
		if _, ok := ParseRelease(raw); ok {
			t.Fatalf("%q must not parse as a release", raw)
		}
	}
}

func TestCountryAcceptsOnlyAlpha2(t *testing.T) {
	if Country(" kr ") != "KR" || Country("US") != "US" {
		t.Fatal("alpha-2 country must be accepted uppercase")
	}
	for _, raw := range []string{"", "KOR", "K", "1A", "<b", "xx1"} {
		if Country(raw) != "" {
			t.Fatalf("Country(%q) must be unknown", raw)
		}
	}
}

func TestOutcomeClasses(t *testing.T) {
	for status, want := range map[int]string{0: OutcomeSuccess, 200: OutcomeSuccess, 304: OutcomeSuccess, 400: OutcomeRejected, 401: OutcomeRejected, 429: OutcomeRejected, 500: OutcomeFailure, 503: OutcomeFailure} {
		if got := Outcome(status); got != want {
			t.Fatalf("Outcome(%d)=%s want %s", status, got, want)
		}
	}
}

func TestIdentifyMatchesAnonymousLedgerHash(t *testing.T) {
	id := strings.Repeat("ab", 32)
	want := sha256.Sum256([]byte("csx-anonymous-v1|" + id))
	r := httptest.NewRequest("GET", "/v1/stats", nil)
	r.Header.Set(AnonymousHeader, id)
	got := Identify(r)
	if got.Auth != AuthAnonymous || got.CallerHash != hex.EncodeToString(want[:]) {
		t.Fatalf("header identity = %+v", got)
	}
	r = httptest.NewRequest("GET", "/v1/stats", nil)
	r.AddCookie(&http.Cookie{Name: AnonymousCookie, Value: id})
	if cookie := Identify(r); cookie != got {
		t.Fatalf("cookie identity %+v != header identity %+v", cookie, got)
	}
	r = httptest.NewRequest("GET", "/v1/stats", nil)
	r.Header.Set(AnonymousHeader, "not-hex")
	if got := Identify(r); got.Auth != AuthUnidentified || got.CallerHash != "" {
		t.Fatalf("invalid id must be unidentified, got %+v", got)
	}
	r = httptest.NewRequest("GET", "/v1/stats", nil)
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set(AnonymousHeader, id)
	auth := Identify(r)
	if auth.Auth != AuthAuthenticated || auth.CallerHash == "" || auth.CallerHash == got.CallerHash || strings.Contains(auth.CallerHash, "secret") {
		t.Fatalf("authenticated identity = %+v", auth)
	}
}

func TestValidRoute(t *testing.T) {
	for _, route := range []string{"GET /v1/stats", "POST /v2/search", "GET /v1/samples/{sampleId}/artifact"} {
		if !ValidRoute(route) {
			t.Fatalf("%q must be a valid route", route)
		}
	}
	for _, route := range []string{"", "/v1/stats", "GET", "GET v1", "GET /v1/\n", "GET /v1/ü", strings.Repeat("A", 100)} {
		if ValidRoute(route) {
			t.Fatalf("%q must be rejected", route)
		}
	}
}

func TestLatencyBucketsAndP95(t *testing.T) {
	if LatencyBucket(0) != 0 || LatencyBucket(10) != 0 || LatencyBucket(11) != 1 || LatencyBucket(5000) != LatencyBuckets-2 || LatencyBucket(5001) != LatencyBuckets-1 {
		t.Fatal("bucket bounds are inclusive upper bounds")
	}
	var buckets [LatencyBuckets]int64
	if bound, over := P95(buckets); bound != 0 || over {
		t.Fatal("empty histogram has no p95")
	}
	// 95 fast, 5 slow: the 95th request is still in the fast bucket.
	buckets[0] = 95
	buckets[LatencyBuckets-1] = 5
	if bound, over := P95(buckets); bound != 10 || over {
		t.Fatalf("p95 = %d over=%v want 10", bound, over)
	}
	// 94 fast, 6 slow: the 95th request is in the overflow bucket.
	buckets[0] = 94
	buckets[LatencyBuckets-1] = 6
	if bound, over := P95(buckets); bound != 5000 || !over {
		t.Fatalf("p95 = %d over=%v want overflow", bound, over)
	}
}

func fixedClock(start time.Time) func() time.Time {
	current := start
	return func() time.Time {
		current = current.Add(3 * time.Millisecond)
		return current
	}
}

func TestCollectorWrapRecordsOneObservationPerRequest(t *testing.T) {
	store := &MemoryStore{}
	now := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC)
	c := New(context.Background(), store, Config{CountryHeader: "X-Country", FlushEvery: time.Hour, Now: fixedClock(now)})
	handler := c.Wrap("POST /v2/search", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	r := httptest.NewRequest("POST", "/v2/search?q=secret", nil)
	r.Header.Set("User-Agent", "csx/v0.1.190 (mcp; protocol=2025-06-18)")
	r.Header.Set("X-Country", "kr")
	r.Header.Set(AnonymousHeader, strings.Repeat("cd", 32))
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if c.Telemetry().Pending != 1 {
		t.Fatalf("pending = %d", c.Telemetry().Pending)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if tel := c.Telemetry(); tel.Flushes != 1 || tel.Pending != 0 || tel.Dropped != 0 || tel.StoreFailures != 0 {
		t.Fatalf("telemetry after close = %+v", tel)
	}
	report, err := store.DemandReport(context.Background(), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Hours) != 1 || report.Hours[0].Outcome != OutcomeRejected || report.Hours[0].Auth != AuthAnonymous || report.Hours[0].Requests != 1 || !report.Hours[0].Hour.Equal(now.Truncate(time.Hour)) {
		t.Fatalf("hours = %+v", report.Hours)
	}
	if len(report.Routes) != 1 || report.Routes[0].Route != "POST /v2/search" || report.Routes[0].Buckets[0] != 1 {
		t.Fatalf("routes = %+v", report.Routes)
	}
	if len(report.Clients) != 1 || report.Clients[0] != (ClientRow{Kind: ClientMCP, Version: "v0.1.190", Protocol: "2025-06-18", Requests: 1}) {
		t.Fatalf("clients = %+v", report.Clients)
	}
	if len(report.Countries) != 1 || report.Countries[0] != (CountryRow{Country: "KR", Requests: 1}) {
		t.Fatalf("countries = %+v", report.Countries)
	}
	if report.UniqueCallers != 1 || len(report.RouteCallers) != 1 || report.RouteCallers[0].Callers != 1 {
		t.Fatalf("callers = %d %+v", report.UniqueCallers, report.RouteCallers)
	}
}

func TestCollectorWithoutCountryHeaderIgnoresClientClaims(t *testing.T) {
	store := &MemoryStore{}
	now := time.Date(2026, 9, 17, 10, 30, 0, 0, time.UTC)
	c := New(context.Background(), store, Config{FlushEvery: time.Hour, Now: fixedClock(now)})
	handler := c.Wrap("GET /v1/stats", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	r := httptest.NewRequest("GET", "/v1/stats", nil)
	r.Header.Set("CF-IPCountry", "US")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	report, _ := store.DemandReport(context.Background(), now)
	if len(report.Countries) != 1 || report.Countries[0].Country != "" {
		t.Fatalf("countries = %+v: an unconfigured header must be the unknown bucket", report.Countries)
	}
	if report.Hours[0].Outcome != OutcomeSuccess || report.Hours[0].Auth != AuthUnidentified || report.UniqueCallers != 0 {
		t.Fatalf("hours = %+v callers=%d", report.Hours, report.UniqueCallers)
	}
	if report.Clients[0].Kind != ClientNone {
		t.Fatalf("clients = %+v", report.Clients)
	}
}

func TestCollectorRejectsCallerControlledRouteLabels(t *testing.T) {
	c := New(context.Background(), &MemoryStore{}, Config{FlushEvery: time.Hour})
	defer c.Close(context.Background())
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	if wrapped := c.Wrap("/v1/../etc", inner); wrapped.(http.HandlerFunc) == nil {
		t.Fatal("expected passthrough")
	}
	wrapped := c.Wrap("/v1/../etc", inner)
	wrapped.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if c.Telemetry().Pending != 0 {
		t.Fatal("an invalid route label must not be observed")
	}
}

func TestCollectorCountsDropsAndStoreFailures(t *testing.T) {
	store := &MemoryStore{FailWrites: true}
	// A collector whose loop has not started: the queue fills deterministically.
	idle := &Collector{store: store, queue: make(chan Observation, 1)}
	idle.Observe(Observation{At: time.Now(), Route: "GET /v1/stats", Outcome: OutcomeSuccess, Auth: AuthUnidentified, ClientKind: ClientNone})
	idle.Observe(Observation{At: time.Now(), Route: "GET /v1/stats", Outcome: OutcomeSuccess, Auth: AuthUnidentified, ClientKind: ClientNone})
	if tel := idle.Telemetry(); tel.Dropped != 1 || tel.Pending != 1 {
		t.Fatalf("telemetry = %+v", tel)
	}
	c := New(context.Background(), store, Config{FlushEvery: time.Hour})
	c.Observe(Observation{At: time.Now(), Route: "GET /v1/stats", Outcome: OutcomeSuccess, Auth: AuthUnidentified, ClientKind: ClientNone})
	if err := c.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tel := c.Telemetry(); tel.StoreFailures != 1 || tel.Flushes != 0 || tel.Pending != 0 {
		t.Fatalf("telemetry = %+v", tel)
	}
}

func TestDisabledCollectorIsIdentity(t *testing.T) {
	var c *Collector
	if c.Available() || c.CountryEnabled() {
		t.Fatal("nil collector must be unavailable")
	}
	disabled := New(context.Background(), nil, Config{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	if disabled.Available() {
		t.Fatal("collector without store must be unavailable")
	}
	disabled.Wrap("GET /v1/stats", inner).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/v1/stats", nil))
	if err := disabled.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryStorePruneRespectsCutoff(t *testing.T) {
	store := &MemoryStore{}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	batch := NewBatch()
	batch.Add(Observation{At: now.Add(-40 * 24 * time.Hour), Route: "GET /v1/stats", Outcome: OutcomeSuccess, Auth: AuthUnidentified, ClientKind: ClientNone, CallerHash: "a"})
	batch.Add(Observation{At: now, Route: "GET /v1/stats", Outcome: OutcomeSuccess, Auth: AuthUnidentified, ClientKind: ClientNone, CallerHash: "b"})
	if err := store.UpsertDemand(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneDemand(context.Background(), now.Add(-DefaultRetention)); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.hourly) != 1 || len(store.clients) != 1 || len(store.countries) != 1 || len(store.callers) != 1 {
		t.Fatalf("prune left hourly=%d clients=%d countries=%d callers=%d", len(store.hourly), len(store.clients), len(store.countries), len(store.callers))
	}
}

func TestSummarizeWindowsAndStaleShare(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 30, 0, 0, time.UTC)
	report := Report{
		Hours: []HourRow{
			{Hour: now.Add(-time.Hour).Truncate(time.Hour), Outcome: OutcomeSuccess, Auth: AuthAnonymous, Requests: 10},
			{Hour: now.Add(-time.Hour).Truncate(time.Hour), Outcome: OutcomeFailure, Auth: AuthAuthenticated, Requests: 2},
			{Hour: now.Add(-2 * 24 * time.Hour).Truncate(time.Hour), Outcome: OutcomeRejected, Auth: AuthUnidentified, Requests: 3},
			{Hour: now.Add(-8 * 24 * time.Hour).Truncate(time.Hour), Outcome: OutcomeSuccess, Auth: AuthAnonymous, Requests: 7},
			{Hour: now.Add(-20 * 24 * time.Hour).Truncate(time.Hour), Outcome: OutcomeSuccess, Auth: AuthAnonymous, Requests: 100},
		},
		Routes: []RouteRow{
			{Route: "POST /v2/search", Outcome: OutcomeSuccess, HourlyCounts: HourlyCounts{Requests: 10, LatencySumMS: 500, Buckets: [LatencyBuckets]int64{0, 0, 9, 1}}},
			{Route: "POST /v2/search", Outcome: OutcomeFailure, HourlyCounts: HourlyCounts{Requests: 2, LatencySumMS: 10000, Buckets: [LatencyBuckets]int64{0, 0, 0, 0, 0, 0, 0, 0, 0, 2}}},
			{Route: "GET /v1/stats", Outcome: OutcomeRejected, HourlyCounts: HourlyCounts{Requests: 3, LatencySumMS: 3, Buckets: [LatencyBuckets]int64{3}}},
		},
		RouteCallers:       []RouteCallers{{Route: "POST /v2/search", Callers: 4}},
		UniqueCallers:      5,
		PriorUniqueCallers: 2,
		Clients: []ClientRow{
			{Kind: ClientMCP, Version: "v0.1.190", Protocol: "2025-06-18", Requests: 6},
			{Kind: ClientCLI, Version: "v0.1.195", Requests: 3},
			{Kind: ClientDaemon, Version: "devgit", Requests: 1},
			{Kind: ClientOther, Requests: 5},
		},
		Countries: []CountryRow{{Country: "KR", Requests: 9}, {Country: "US", Requests: 3}, {Country: "", Requests: 3}},
	}
	s := Summarize(report, now, "v0.1.195")
	if s.Week.Total != 15 || s.Week.Success != 10 || s.Week.Failure != 2 || s.Week.Rejected != 3 || s.Week.Anonymous != 10 || s.Week.Authenticated != 2 || s.Week.Unidentified != 3 || s.Week.UniqueCallers != 5 {
		t.Fatalf("week = %+v", s.Week)
	}
	if s.PriorWeek.Total != 7 || s.PriorWeek.UniqueCallers != 2 {
		t.Fatalf("prior week = %+v", s.PriorWeek)
	}
	if len(s.Days) != ReportDays || s.Days[ReportDays-1].Day != "2026-09-17" || s.Days[ReportDays-1].Total != 12 || !s.Days[ReportDays-1].Observed || s.Days[ReportDays-3].Total != 3 || s.Days[0].Observed {
		t.Fatalf("days = %+v", s.Days)
	}
	if len(s.Last24Hours) != 24 || s.Last24Hours[22].Total != 12 || s.Last24Hours[23].Total != 0 {
		t.Fatalf("last 24 hours = %+v", s.Last24Hours)
	}
	if s.HourOfDay[11] != 12 || s.HourOfDay[12] != 3 {
		t.Fatalf("hour of day = %v", s.HourOfDay)
	}
	if len(s.Endpoints) != 2 || s.Endpoints[0].Route != "POST /v2/search" || s.Endpoints[0].Calls != 12 || s.Endpoints[0].UniqueCallers != 4 || s.Endpoints[0].P95BoundMS != 5000 || !s.Endpoints[0].P95Over || s.Endpoints[0].MeanMS != 875 || s.Endpoints[1].P95BoundMS != 10 {
		t.Fatalf("endpoints = %+v", s.Endpoints)
	}
	if rate := s.Endpoints[0].FailureRate; rate < 0.166 || rate > 0.167 {
		t.Fatalf("failure rate = %v", rate)
	}
	if s.Identified != 10 || s.StaleRequests != 6 || s.CurrentRequests != 3 {
		t.Fatalf("identified=%d stale=%d current=%d", s.Identified, s.StaleRequests, s.CurrentRequests)
	}
	if len(s.Clients) != 4 || s.Clients[0].Kind != ClientMCP || !s.Clients[0].Stale || s.Clients[0].Share != 0.4 {
		t.Fatalf("clients = %+v", s.Clients)
	}
	if len(s.Protocols) != 1 || s.Protocols[0].Protocol != "2025-06-18" || s.Protocols[0].Share != 1 {
		t.Fatalf("protocols = %+v", s.Protocols)
	}
	if len(s.Kinds) != 4 || s.Kinds[0].Kind != ClientMCP {
		t.Fatalf("kinds = %+v", s.Kinds)
	}
	if len(s.Countries) != 2 || s.Countries[0].Country != "KR" || s.CountryTotal != 15 || s.CountryUnknown != 3 || s.Countries[0].Share != 0.6 {
		t.Fatalf("countries = %+v total=%d unknown=%d", s.Countries, s.CountryTotal, s.CountryUnknown)
	}
	dev := Summarize(report, now, "dev")
	if dev.StaleRequests != 0 || dev.CurrentRequests != 0 || dev.Identified != 10 {
		t.Fatalf("a non-release server must not call any build stale: %+v", dev)
	}
}
