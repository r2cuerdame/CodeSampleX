package admin

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAccessLogMetricsAggregateFixedRoutesMethodsStatusesAndDays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 18, 0, 0, 0, time.UTC)
	secret := "customer-secret-query"
	dynamicID := "customer-7f13c5-sensitive-id"
	lines := []string{
		accessJSONLine(t, now.AddDate(0, 0, -1), "GET", "/v1/search?q="+secret, 200, secret),
		accessJSONLine(t, now.Add(-6*time.Hour), "GET", "/v1/wanted", 200, ""),
		accessJSONLine(t, now.Add(-5*time.Hour), "POST", "/v1/wanted?from="+secret, 429, secret),
		accessJSONLine(t, now.Add(-4*time.Hour), "HEAD", "/v1/samples/sha256:"+dynamicID, 404, dynamicID),
		accessJSONLine(t, now.Add(-3*time.Hour), "POST", "/v1/evidence/batches", 500, ""),
		accessJSONLine(t, now.Add(-2*time.Hour), "DELETE", "/v1/new/"+dynamicID, 204, dynamicID),
		accessJSONLine(t, now.Add(-time.Hour), "GET", "/v1/stats", 302, ""),
		accessJSONLine(t, now, "GET", "/healthz", 500, ""),
		accessJSONLine(t, now, "GET", "/npm/private-looking-name", 200, ""),
	}
	writeLines(t, path, lines)
	started := now.AddDate(0, 0, -2).Truncate(time.Second)
	if err := os.WriteFile(path+".since", []byte(started.Format(time.RFC3339)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reader := NewAccessLogReader(path)
	metrics, err := reader.Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := metrics.Totals.Requests, int64(6); got != want {
		t.Fatalf("requests = %d, want %d", got, want)
	}
	if got, want := metrics.Totals.GetHeadRequests, int64(4); got != want {
		t.Errorf("GET/HEAD = %d, want %d", got, want)
	}
	if got, want := metrics.Totals.PostRequests, int64(2); got != want {
		t.Errorf("POST = %d, want %d", got, want)
	}
	if got, want := metrics.Totals.OtherMethodRequests, int64(0); got != want {
		t.Errorf("other methods = %d, want %d", got, want)
	}
	if got := metrics.Totals; got.Status2xx != 2 || got.Status3xx != 1 || got.Status4xx != 2 || got.Status429 != 1 || got.Status5xx != 1 {
		t.Errorf("status counts = %+v, want 2xx=2 3xx=1 4xx=2 429=1 5xx=1", got)
	}
	if !metrics.CollectionStartedAt.Equal(started) {
		t.Errorf("collection start = %s, want %s", metrics.CollectionStartedAt, started)
	}
	if metrics.SourceFiles != 1 || metrics.DaysWithRequests != 2 || len(metrics.Days) != accessLogWindowDays {
		t.Errorf("coverage = files:%d days:%d len:%d", metrics.SourceFiles, metrics.DaysWithRequests, len(metrics.Days))
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteWanted); got.Requests != 2 || got.GetHeadRequests != 1 || got.PostRequests != 1 || got.Status429 != 1 {
		t.Errorf("wanted route = %+v", got)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupConsumption).Requests; got != 2 {
		t.Errorf("consumption requests = %d, want 2", got)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupContribution).Requests; got != 2 {
		t.Errorf("contribution requests = %d, want 2", got)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupCoordination).Requests; got != 2 {
		t.Errorf("coordination requests = %d, want 2", got)
	}

	// The aggregate has only fixed labels and counters. Neither the query,
	// dynamic path ID nor deliberately injected raw-log PII survives it.
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{secret, dynamicID, "203.0.113.77", "Authorization", "Bearer"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("aggregate retained forbidden raw value %q", forbidden)
		}
	}
}

func TestAccessLogUsesOnlyFixedRouteFieldAndNeverRawOrEncodedURI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	secret := "encoded-customer-id-must-not-survive"
	methodSecret := "PRIVATE_METHOD_SECRET"
	lines := []string{
		// Caddy's decoded path matcher may accept an encoded slash. The
		// logger appends only this fixed class and deletes URI; retaining a
		// raw URI here deliberately proves the parser has the same boundary.
		accessJSONLine(t, now, "GET", "/v1/samples%2F"+secret+"/tail?token="+secret, 200, secret, "samples"),
		// A forged/non-allowlisted class is ignored even when the discarded
		// URI itself looks like a supported route.
		accessJSONLine(t, now, "GET", "/v1/search?token="+secret, 200, secret, secret),
		// The raw HTTP method is discarded by Caddy too; only the fixed
		// "other" bucket reaches this parser.
		accessJSONLine(t, now, methodSecret, "/v1/stats", 400, methodSecret),
	}
	writeLines(t, path, lines)

	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Totals.Requests != 2 || metrics.Totals.OtherMethodRequests != 1 || routeByClass(t, metrics.Routes, AccessRouteSamples).Requests != 1 {
		t.Fatalf("fixed route aggregate = requests:%d samples:%d", metrics.Totals.Requests, routeByClass(t, metrics.Routes, AccessRouteSamples).Requests)
	}
	encoded, _ := json.Marshal(metrics)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), methodSecret) || strings.Contains(string(encoded), "%2F") {
		t.Fatal("raw or encoded URI data survived fixed-label aggregation")
	}
}

func TestAccessLogSkipsMalformedAndOversizeLinesWithoutLosingNextLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	oversizeSecret := "oversize-secret-never-retain"
	content := "not json\n" +
		`{"ts":"wrong type","status":200,"request":{"method":"GET","uri":"/v1/search"}}` + "\n" +
		`{"padding":"` + strings.Repeat("x", accessLogLineLimit) + oversizeSecret + `"}` + "\n" +
		accessJSONLine(t, now, "POST", "/v1/search", 200, "") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.MalformedLines != 2 || metrics.OversizeLines != 1 || metrics.Totals.Requests != 1 {
		t.Fatalf("metrics = malformed:%d oversize:%d requests:%d", metrics.MalformedLines, metrics.OversizeLines, metrics.Totals.Requests)
	}
	encoded, _ := json.Marshal(metrics)
	if strings.Contains(string(encoded), oversizeSecret) {
		t.Fatal("oversize raw line survived aggregation")
	}
}

func TestAccessLogReadsGzipAndOnlyNewestFortyRolls(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	baseMod := now.Add(-2 * time.Hour)
	for i := 0; i < accessLogRolledLimit+1; i++ {
		name := filepath.Join(dir, fmt.Sprintf("access-safe-%02d.log", i))
		line := accessJSONLine(t, now.Add(-time.Duration(i)*time.Minute), "GET", "/v1/stats", 200, "")
		if i == 0 {
			line = accessJSONLine(t, now, "GET", "/v1/stats", 500, "excluded-old-roll")
		}
		if i == accessLogRolledLimit {
			name += ".gz"
			writeGzipLines(t, name, []string{line})
		} else {
			writeLines(t, name, []string{line})
		}
		mod := baseMod.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	writeLines(t, path, []string{accessJSONLine(t, now, "GET", "/v1/wanted", 200, "")})
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}

	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.SourceFiles != accessLogRolledLimit+1 {
		t.Errorf("source files = %d, want %d", metrics.SourceFiles, accessLogRolledLimit+1)
	}
	if metrics.Totals.Requests != int64(accessLogRolledLimit+1) {
		t.Errorf("requests = %d, want %d", metrics.Totals.Requests, accessLogRolledLimit+1)
	}
	if metrics.Totals.Status5xx != 0 {
		t.Errorf("oldest excluded roll leaked into totals: %+v", metrics.Totals)
	}
}

func TestAccessLogFiveMinuteCacheAndUnchangedRollReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	writeLines(t, path, []string{accessJSONLine(t, now, "GET", "/v1/search", 200, "")})

	clock := now
	reader := NewAccessLogReader(path)
	reader.clock = func() time.Time { return clock }
	first, err := reader.Metrics(context.Background(), now)
	if err != nil || first.Totals.Requests != 1 {
		t.Fatalf("first = %+v, %v", first.Totals, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := fmt.Fprintln(f, accessJSONLine(t, now, "POST", "/v1/wanted", 201, ""))
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append: %v; close: %v", writeErr, closeErr)
	}

	clock = clock.Add(4 * time.Minute)
	withinTTL, err := reader.Metrics(context.Background(), now)
	if err != nil || withinTTL.Totals.Requests != 1 {
		t.Fatalf("cached = %+v, %v", withinTTL.Totals, err)
	}
	clock = clock.Add(2 * time.Minute)
	afterTTL, err := reader.Metrics(context.Background(), now)
	if err != nil || afterTTL.Totals.Requests != 2 {
		t.Fatalf("refreshed = %+v, %v", afterTTL.Totals, err)
	}
}

func TestAccessLogGroupsUseRouteAndMethodSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	lines := []string{
		accessJSONLine(t, now, "POST", "/v1/search", 200, ""),
		accessJSONLine(t, now, "GET", "/v1/samples/id", 200, ""),
		accessJSONLine(t, now, "POST", "/v1/samples", 201, ""),
		accessJSONLine(t, now, "GET", "/v1/wanted", 200, ""),
		accessJSONLine(t, now, "POST", "/v1/wanted", 202, ""),
		accessJSONLine(t, now, "POST", "/v1/verifications", 201, ""),
		accessJSONLine(t, now, "GET", "/v1/evidence", 405, ""),
		accessJSONLine(t, now, "GET", "/v1/verification/jobs", 200, ""),
		accessJSONLine(t, now, "POST", "/v1/verification/jobs/7/claim", 200, ""),
		accessJSONLine(t, now, "POST", "/v1/peers/announce", 204, ""),
	}
	writeLines(t, path, lines)
	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupConsumption).Requests; got != 2 {
		t.Errorf("consumption = %d, want 2", got)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupContribution).Requests; got != 3 {
		t.Errorf("contribution = %d, want 3", got)
	}
	if got := groupByClass(t, metrics.Groups, AccessGroupCoordination).Requests; got != 5 {
		t.Errorf("coordination = %d, want 5", got)
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteSamples).GroupLabel; got != "조회·게시 혼합" {
		t.Errorf("samples group label = %q", got)
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteWanted).GroupLabel; got != "조회·보고 혼합" {
		t.Errorf("wanted group label = %q", got)
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteEvidence).GroupLabel; got != "POST 기여 · 기타 조정" {
		t.Errorf("evidence group label = %q", got)
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteVerifications).Requests; got != 1 {
		t.Errorf("verification submissions = %d, want 1", got)
	}
	if got := routeByClass(t, metrics.Routes, AccessRouteVerificationJobs).Requests; got != 2 {
		t.Errorf("verification coordination = %d, want 2", got)
	}
}

func TestAccessLogRetentionBoundaryMatchesTheThirtyOneDayAggregate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	inWindow := now.Add(-time.Hour)
	lines := []string{
		accessJSONLine(t, now.AddDate(0, 0, -31), "GET", "/v1/stats", 200, "old-dynamic-id"),
		accessJSONLine(t, inWindow, "GET", "/v1/stats", 200, ""),
		accessJSONLine(t, now.Add(time.Hour), "GET", "/v1/stats", 200, "future-dynamic-id"),
	}
	writeLines(t, path, lines)
	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Totals.Requests != 1 || !metrics.OldestEventAt.Equal(inWindow) || !metrics.NewestEventAt.Equal(inWindow) {
		t.Fatalf("window mismatch: requests=%d oldest=%s newest=%s", metrics.Totals.Requests, metrics.OldestEventAt, metrics.NewestEventAt)
	}
	encoded, _ := json.Marshal(metrics)
	if strings.Contains(string(encoded), "old-dynamic-id") || strings.Contains(string(encoded), "future-dynamic-id") {
		t.Fatal("out-of-window dynamic data survived aggregation")
	}
}

func TestAccessLogRejectsOldUnsafePathAndTreatsMissingSafeLogAsEmpty(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	_, err := NewAccessLogReader(filepath.Join(dir, "access.log")).Metrics(context.Background(), now)
	if !errors.Is(err, errUnsafeAccessLogPath) {
		t.Fatalf("unsafe path error = %v", err)
	}
	empty, err := NewAccessLogReader(filepath.Join(dir, "access-safe.log")).Metrics(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if empty.SourceFiles != 0 || empty.Totals.Requests != 0 || len(empty.Days) != accessLogWindowDays {
		t.Fatalf("empty metrics = files:%d requests:%d days:%d", empty.SourceFiles, empty.Totals.Requests, len(empty.Days))
	}
}

func TestAccessLogBoundsUnexpectedFileAndReturnsGenericGzipError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	oversize := strings.Repeat("x", accessLogFileLimit+2)
	if err := os.WriteFile(path, []byte(oversize), 0o600); err != nil {
		t.Fatal(err)
	}
	metrics, err := NewAccessLogReader(path).Metrics(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if metrics.TruncatedFiles != 1 || metrics.OversizeLines != 1 {
		t.Fatalf("bounded file diagnostics = truncated:%d oversize:%d", metrics.TruncatedFiles, metrics.OversizeLines)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	badGzip := filepath.Join(dir, "access-safe-2026-08-17.log.gz")
	secret := "gzip-secret-must-not-appear"
	if err := os.WriteFile(badGzip, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = NewAccessLogReader(path).Metrics(context.Background(), time.Now())
	if !errors.Is(err, errAccessLogGzip) || strings.Contains(fmt.Sprint(err), secret) || strings.Contains(fmt.Sprint(err), badGzip) {
		t.Fatalf("gzip error was not generic: %v", err)
	}
}

func TestAccessLogHonorsCanceledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access-safe.log")
	writeLines(t, path, []string{accessJSONLine(t, time.Now(), "GET", "/v1/stats", 200, "")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewAccessLogReader(path).Metrics(ctx, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func accessJSONLine(t *testing.T, when time.Time, method, uri string, status int, injectedSecret string, routeOverride ...string) string {
	t.Helper()
	entry := map[string]any{
		"ts":     float64(when.UnixNano()) / float64(time.Second),
		"status": status,
		"request": map[string]any{
			"method":    method,
			"uri":       uri,
			"remote_ip": "203.0.113.77",
			"headers": map[string]any{
				"Authorization": []string{"Bearer " + injectedSecret},
			},
		},
		"user_id": injectedSecret,
	}
	switch method {
	case "GET", "HEAD":
		entry["csx_method"] = "get_head"
	case "POST":
		entry["csx_method"] = "post"
	default:
		entry["csx_method"] = "other"
	}
	route := testAccessRouteLabel(uri)
	if len(routeOverride) > 0 {
		route = routeOverride[0]
	}
	if route != "" {
		entry["csx_route"] = route
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func testAccessRouteLabel(uri string) string {
	path, _, _ := strings.Cut(uri, "?")
	prefix := func(value string) bool { return path == value || strings.HasPrefix(path, value+"/") }
	switch {
	case path == "/v1/search":
		return "search"
	case prefix("/v1/evidence"):
		return "evidence"
	case prefix("/v1/registry"):
		return "registry"
	case prefix("/v1/shards"):
		return "shards"
	case prefix("/v1/samples"):
		return "samples"
	case prefix("/v1/wanted"):
		return "wanted"
	case prefix("/v1/adoptions"):
		return "adoption"
	case prefix("/v1/verifications"):
		return "verifications"
	case prefix("/v1/verification"):
		return "verification_jobs"
	case prefix("/v1/peers"):
		return "peers"
	case path == "/v1/stats":
		return "stats"
	case path == "/v1/adapters":
		return "adapters"
	case prefix("/v1/auth"):
		return "auth"
	default:
		return ""
	}
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGzipLines(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(f)
	_, writeErr := compressed.Write([]byte(strings.Join(lines, "\n") + "\n"))
	closeGzipErr := compressed.Close()
	closeFileErr := f.Close()
	if writeErr != nil || closeGzipErr != nil || closeFileErr != nil {
		t.Fatalf("gzip write: %v; gzip close: %v; file close: %v", writeErr, closeGzipErr, closeFileErr)
	}
}

func routeByClass(t *testing.T, routes []AccessRouteCounts, class AccessRouteClass) AccessRouteCounts {
	t.Helper()
	for _, route := range routes {
		if route.Class == class {
			return route
		}
	}
	t.Fatalf("missing route %q", class)
	return AccessRouteCounts{}
}

func groupByClass(t *testing.T, groups []AccessGroupCounts, class AccessActivityGroup) AccessGroupCounts {
	t.Helper()
	for _, group := range groups {
		if group.Group == class {
			return group
		}
	}
	t.Fatalf("missing group %q", class)
	return AccessGroupCounts{}
}
