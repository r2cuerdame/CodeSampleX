package admin

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/apidemand"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

var demandTestNow = time.Date(2026, 8, 17, 12, 30, 0, 0, time.UTC)

// demandFixture feeds one scripted week into the real fake store, so the
// rendered panel is what the collector, the store and the summary produce
// together, not a hand-typed view.
func demandFixture(t *testing.T) (*apidemand.Collector, *serverstore.Fake) {
	t.Helper()
	store := serverstore.NewFake()
	store.NowFn = func() time.Time { return demandTestNow }
	batch := apidemand.NewBatch()
	obs := func(at time.Time, route, outcome, auth, caller, country, kind, version, protocol string, latency int64) {
		batch.Add(apidemand.Observation{At: at, Route: route, Outcome: outcome, Auth: auth, CallerHash: caller, Country: country, ClientKind: kind, ClientVersion: version, Protocol: protocol, LatencyMS: latency})
	}
	for i := 0; i < 40; i++ {
		obs(demandTestNow.Add(-time.Duration(i)*time.Hour), "POST /v2/search", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "caller-a", "KR", apidemand.ClientMCP, "v0.1.190", "2025-06-18", 35)
	}
	obs(demandTestNow.Add(-2*time.Hour), "POST /v2/search", apidemand.OutcomeFailure, apidemand.AuthAuthenticated, "caller-b", "US", apidemand.ClientCLI, "v0.1.195", "", 7000)
	obs(demandTestNow.Add(-3*time.Hour), "POST /v2/search", apidemand.OutcomeRejected, apidemand.AuthUnidentified, "", "", apidemand.ClientOther, "", "", 2)
	obs(demandTestNow.Add(-26*time.Hour), "GET /v1/stats", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "caller-c", "DE", apidemand.ClientDaemon, "v0.1.195", "", 12)
	obs(demandTestNow.Add(-9*24*time.Hour), "GET /v1/stats", apidemand.OutcomeSuccess, apidemand.AuthAnonymous, "caller-d", "DE", apidemand.ClientDaemon, "devgit", "", 12)
	if err := store.UpsertDemand(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertPackage(ctx, serverstore.PackageRow{PURL: "pkg:npm/axios@1.12.0", Ecosystem: "npm", Name: "axios", Version: "1.12.0", Major: "1", Publicness: "PUBLIC"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSample(ctx, serverstore.SampleRow{SampleID: "sha256:" + strings.Repeat("a1", 32), ManifestJSON: `{"packages":["pkg:npm/axios@1.12.0"],"goal":"get","symbols":["axios.get"]}`, SizeBytes: 10, CreatedAt: demandTestNow}); err != nil {
		t.Fatal(err)
	}
	day := func(offset int) string { return demandTestNow.AddDate(0, 0, -offset).Format("2006-01-02") }
	if err := store.RecordWantedBatch(ctx, []serverstore.WantedSubmission{
		{Epoch: day(0), AnonID: "a", Rows: []serverstore.WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post"}, {Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
		{Epoch: day(1), AnonID: "b", Rows: []serverstore.WantedRow{{Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
		{Epoch: day(8), AnonID: "c", Rows: []serverstore.WantedRow{{Ecosystem: "pypi", Name: "requests", Symbol: "Session.get"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSearchHit(ctx, serverstore.SearchHitRow{Grade: "VERIFIED", ResultsShown: 1, SampleID: "sha256:" + strings.Repeat("a1", 32), OfferID: "o1", Epoch: day(0), AnonID: "a"}); err != nil {
		t.Fatal(err)
	}
	collector := apidemand.New(context.Background(), store, apidemand.Config{CountryHeader: "X-Edge-Country", FlushEvery: time.Hour, Now: func() time.Time { return demandTestNow }})
	t.Cleanup(func() { collector.Close(context.Background()) })
	return collector, store
}

func demandMux(t *testing.T, demand DemandReader, insights serverstore.AdminDemandReader) (*http.ServeMux, string) {
	t.Helper()
	secret := "a-long-random-admin-secret"
	mux := http.NewServeMux()
	if !Register(mux, Deps{
		Store:          &fakeStore{},
		TokenSHA256:    digest(secret),
		PublicURL:      "https://codesamplex.dev",
		Version:        "abc1234",
		ReleaseVersion: "v0.1.195",
		StartedAt:      demandTestNow.Add(-time.Hour),
		Now:            func() time.Time { return demandTestNow },
		Demand:         demand,
		DemandInsights: insights,
	}) {
		t.Fatal("valid token hash did not register /admin")
	}
	return mux, secret
}

func TestDemandPanelRendersMeasuredTelemetryExpanded(t *testing.T) {
	collector, store := demandFixture(t)
	mux, secret := demandMux(t, collector, store)
	rec := serve(mux, http.MethodGet, "/admin", "recuerdame", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	start := strings.Index(body, `<section id="demand-diagnostics"`)
	end := strings.Index(body[start:], `</section>`)
	if start < 0 || end < 0 {
		t.Fatal("demand panel missing")
	}
	panel := body[start : start+end]

	for _, want := range []string{
		// Week totals: 40 search hits + failure + rejected + stats within 7d = 43.
		`<span class="label">최근 7일 API 호출</span><span class="value">43</span>`,
		`<span class="label">실패 · 5xx</span><span class="value bad">1</span>`,
		`<span class="label">거절 · 4xx</span><span class="value">1</span>`,
		`<span class="label">고유 호출자 · 7일</span><span class="value">3</span>`,
		`>41 / 1 / 1<`,
		// Week-over-week: the prior window holds exactly the one 9-day-old call.
		`<td>전체 호출</td><td>43</td><td>1</td><td class="delta-cell positive">&#43;42 · &#43;4200.0%</td>`,
		`<td>실패 · 5xx</td><td>1</td><td>0</td><td class="delta-cell negative">&#43;1 · 이전 주 0</td>`,
		`<td>고유 호출자</td><td>3</td><td>1</td><td class="delta-cell positive">&#43;2 · &#43;200.0%</td>`,
		// Endpoints: search dominates, p95 within the 50 ms bound, 1/42 failed.
		`<td><code>POST /v2/search</code></td><td>42</td><td>2</td><td class="bad">1</td><td>2.4%</td><td>2.4%</td><td>≤ 50 ms</td>`,
		`<td><code>GET /v1/stats</code></td><td>1</td><td>1</td><td>0</td><td>0.0%</td><td>0.0%</td><td>≤ 25 ms</td><td>12 ms</td>`,
		// Countries from the trusted edge header, unknown counted separately.
		`<td><code>KR</code></td><td>40</td><td>93.0%</td>`,
		`국가 확인 97.7% · 미확인 1건`,
		// Clients: MCP on an older build is stale against v0.1.195.
		`<td>MCP</td><td><code>v0.1.190</code></td><td>40</td><td>93.0%</td><td class="state-stale">구버전</td>`,
		`<td>CLI</td><td><code>v0.1.195</code></td><td>1</td><td>2.3%</td><td class="state-current">최신</td>`,
		`<td><code>2025-06-18</code></td><td>40</td><td>100.0%</td>`,
		`<span class="label">구버전 비중</span><span class="value small warn">95.2%</span>`,
		// Package demand against sample supply.
		`<td><code>pypi/requests</code></td><td>2</td><td>1</td><td>&#43;1</td><td>1</td><td class="warn">0</td>`,
		`<td><code>npm/axios</code></td><td>1</td><td>0</td><td>&#43;1</td><td>1</td><td class="warn">1</td>`,
		`<td><code>pypi/requests</code></td><td>2</td><td>1</td><td class="warn">0</td>`,
		`<td><code>pypi/requests</code></td><td>—</td><td><code>Session.get</code></td><td>—</td><td>2</td>`,
		// Search outcomes in the same unit.
		`<span class="label">검색 결과 있음 · HIT</span><span class="value">1</span>`,
		`<span class="label">검색 결과 없음 · NO_SAFE_MATCH</span><span class="value">2</span>`,
		`<td>검색 전체</td><td>3</td><td>1</td><td class="delta-cell positive">&#43;2 · &#43;200.0%</td>`,
		`수집 시작 2026-08-08 12:00 UTC · 국가는 에지 헤더 기준`,
		`class="seg-success"`, `class="seg-failure"`, `class="seg-hit"`,
	} {
		if !strings.Contains(panel, want) {
			t.Errorf("panel is missing %q", want)
		}
	}
	if t.Failed() {
		t.Log(panel)
		t.FailNow()
	}
	// Primary tables are expanded: only the long lists sit behind details.
	for _, summary := range []string{"주간 대비", "상위 API 엔드포인트", "국가별 사용", "패키지·라이브러리 수요 TOP", "결과 없음 수요 TOP"} {
		if regexp.MustCompile(`<summary>[^<]*` + regexp.QuoteMeta(summary)).MatchString(panel) {
			t.Fatalf("%q is folded behind a details element", summary)
		}
	}
	if !strings.Contains(panel, `<summary>일별 검색 결과 표 · 14일</summary>`) {
		t.Fatal("the fourteen-day table must be the folded long list")
	}
	// Nothing user-controlled reaches the page: no raw path, hash, or agent.
	for _, leak := range []string{"caller-a", "curl", "Mozilla", "?q="} {
		if strings.Contains(panel, leak) {
			t.Fatalf("panel leaked %q", leak)
		}
	}
}

func TestDemandPanelSaysNotMeasuredInsteadOfZeros(t *testing.T) {
	mux, secret := demandMux(t, nil, nil)
	rec := serve(mux, http.MethodGet, "/admin", "recuerdame", secret)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"API 수요 텔레메트리 수집기가 구성되지 않았습니다",
		"이 저장소는 패키지 수요 집계를 제공하지 않습니다",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(body, `<span class="label">최근 7일 API 호출</span>`) {
		t.Fatal("an unconfigured collector must not render zero tiles")
	}

	// A collector without a store is the same as none; a typed nil pointer
	// in the interface must not panic the page.
	var typedNil *apidemand.Collector
	mux, secret = demandMux(t, typedNil, nil)
	if rec := serve(mux, http.MethodGet, "/admin", "recuerdame", secret); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "API 수요 텔레메트리 수집기가 구성되지 않았습니다") {
		t.Fatalf("typed nil collector: %d", rec.Code)
	}

	// Country dimension off: the panel says why and never shows a table of
	// client-claimed countries.
	_, store := demandFixture(t)
	noCountry := apidemand.New(context.Background(), store, apidemand.Config{FlushEvery: time.Hour})
	t.Cleanup(func() { noCountry.Close(context.Background()) })
	mux, secret = demandMux(t, noCountry, store)
	body = serve(mux, http.MethodGet, "/admin", "recuerdame", secret).Body.String()
	if !strings.Contains(body, "국가 헤더가 구성되지 않았습니다 (CSX_COUNTRY_HEADER 비어 있음)") || strings.Contains(body, `<td><code>KR</code></td>`) {
		t.Fatal("country panel must be disabled without a trusted header")
	}
}

func TestDemandDeltaTonesFollowTheOperatorsInterest(t *testing.T) {
	if d := demandDeltaOf("x", 10, 5, true); d.Tone != "positive" || d.Delta != "+5 · +100.0%" {
		t.Fatalf("more good = %+v", d)
	}
	if d := demandDeltaOf("x", 10, 5, false); d.Tone != "negative" {
		t.Fatalf("more bad = %+v", d)
	}
	if d := demandDeltaOf("x", 2, 5, false); d.Tone != "positive" || d.Delta != "-3 · -60.0%" {
		t.Fatalf("fewer bad = %+v", d)
	}
	if d := demandDeltaOf("x", 0, 0, true); d.Tone != "zero" || d.Delta != "—" {
		t.Fatalf("nothing = %+v", d)
	}
	if d := demandDeltaOf("x", 3, 0, true); d.Delta != "+3 · 이전 주 0" {
		t.Fatalf("first week = %+v", d)
	}
}

func TestDemandStackChartScalesToTheLargestBar(t *testing.T) {
	chart := buildDemandStackChart("t", []demandStackBar{
		{Label: "a", Observed: true, Segments: []demandStackSegment{{Class: "seg-success", Value: 30}, {Class: "seg-failure", Value: 10}}},
		{Label: "b", Observed: true, Segments: []demandStackSegment{{Class: "seg-success", Value: 20}}},
		{Label: "c"},
	}, nil)
	if chart.Empty || chart.Max != 40 {
		t.Fatalf("chart = %+v", chart)
	}
	a := chart.Bars[0]
	if a.Segments[0].Height != 72 || a.Segments[0].Y != 40 || a.Segments[1].Height != 24 || a.Segments[1].Y != 16 {
		t.Fatalf("stacked segments = %+v", a.Segments)
	}
	if b := chart.Bars[1]; b.Segments[0].Height != 48 || b.Segments[0].Y != 64 {
		t.Fatalf("second bar = %+v", b.Segments)
	}
	if chart.Bars[2].Total != 0 || chart.Bars[2].X <= chart.Bars[1].X {
		t.Fatalf("empty bar keeps its slot: %+v", chart.Bars[2])
	}
	if empty := buildDemandStackChart("t", nil, nil); !empty.Empty {
		t.Fatal("no bars must be empty")
	}
}
