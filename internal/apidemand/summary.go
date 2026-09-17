package apidemand

import (
	"sort"
	"time"
)

// ReportDays is how many UTC calendar days the daily series shows.
const ReportDays = 7

// WindowTotals is one rolling window's counts.
type WindowTotals struct {
	Total         int64
	Success       int64
	Failure       int64
	Rejected      int64
	Anonymous     int64
	Authenticated int64
	Unidentified  int64
	UniqueCallers int64
}

func (w *WindowTotals) add(row HourRow) {
	w.Total += row.Requests
	switch row.Outcome {
	case OutcomeSuccess:
		w.Success += row.Requests
	case OutcomeFailure:
		w.Failure += row.Requests
	case OutcomeRejected:
		w.Rejected += row.Requests
	}
	switch row.Auth {
	case AuthAnonymous:
		w.Anonymous += row.Requests
	case AuthAuthenticated:
		w.Authenticated += row.Requests
	default:
		w.Unidentified += row.Requests
	}
}

// DaySummary is one UTC calendar day. Observed distinguishes a day with any
// retained row from a zero-filled chart position before collection began.
type DaySummary struct {
	Day      string
	Observed bool
	WindowTotals
}

// HourSummary is one UTC hour of the last twenty-four.
type HourSummary struct {
	Hour time.Time
	WindowTotals
}

// EndpointSummary is one fixed route over the last seven days.
type EndpointSummary struct {
	Route         string
	Calls         int64
	Success       int64
	Failure       int64
	Rejected      int64
	UniqueCallers int64
	// FailureRate is Failure / Calls; RejectedRate is Rejected / Calls.
	FailureRate  float64
	RejectedRate float64
	// P95BoundMS is the histogram bound the 95th percentile falls under;
	// P95Over means it fell into the overflow bucket, i.e. slower than the
	// last bound. MeanMS is exact.
	P95BoundMS int64
	P95Over    bool
	MeanMS     int64
}

// ClientSummary is one client build over the last seven days.
type ClientSummary struct {
	Kind     string
	Version  string
	Protocol string
	Requests int64
	Share    float64
	// Stale is true when the version parses as a release older than the
	// server's own; Current when it is the same or newer; neither for a
	// build that is not a release.
	Stale   bool
	Current bool
}

// KindSummary is one client kind's share.
type KindSummary struct {
	Kind     string
	Requests int64
	Share    float64
}

// ProtocolSummary is one MCP protocol revision's share among MCP requests.
type ProtocolSummary struct {
	Protocol string
	Requests int64
	Share    float64
}

// CountrySummary is one edge-reported country over the last seven days.
type CountrySummary struct {
	Country  string
	Requests int64
	Share    float64
}

// Summary is everything the dashboard shows, derived from one Report.
type Summary struct {
	Days        []DaySummary
	Last24Hours []HourSummary
	// HourOfDay is the seven-day request total per UTC hour of day.
	HourOfDay [24]int64
	Week      WindowTotals
	PriorWeek WindowTotals
	Endpoints []EndpointSummary
	Clients   []ClientSummary
	Kinds     []KindSummary
	Protocols []ProtocolSummary
	// Identified is the number of requests whose client was a csx build;
	// StaleRequests and CurrentRequests partition the ones whose version is
	// a comparable release. ServerRelease is the yardstick; when it is not
	// a release, no build is stale.
	Identified      int64
	StaleRequests   int64
	CurrentRequests int64
	ServerRelease   string
	Countries       []CountrySummary
	CountryTotal    int64
	CountryUnknown  int64
	OldestHour      time.Time
}

// Summarize derives the dashboard summary. now is the end of every rolling
// window; serverVersion decides which client builds are stale.
func Summarize(report Report, now time.Time, serverVersion string) Summary {
	now = now.UTC()
	weekStart := now.Add(-7 * 24 * time.Hour)
	priorStart := now.Add(-14 * 24 * time.Hour)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	firstDay := today.AddDate(0, 0, -(ReportDays - 1))
	currentHour := now.Truncate(time.Hour)
	first24 := currentHour.Add(-23 * time.Hour)

	summary := Summary{OldestHour: report.OldestHour, ServerRelease: serverVersion}
	summary.Days = make([]DaySummary, ReportDays)
	dayIndex := make(map[string]int, ReportDays)
	for i := range summary.Days {
		day := firstDay.AddDate(0, 0, i).Format("2006-01-02")
		summary.Days[i].Day = day
		dayIndex[day] = i
	}
	summary.Last24Hours = make([]HourSummary, 24)
	for i := range summary.Last24Hours {
		summary.Last24Hours[i].Hour = first24.Add(time.Duration(i) * time.Hour)
	}

	for _, row := range report.Hours {
		hour := row.Hour.UTC()
		if !hour.Before(weekStart) && hour.Before(now.Add(time.Hour)) {
			summary.Week.add(row)
			summary.HourOfDay[hour.Hour()] += row.Requests
		} else if !hour.Before(priorStart) && hour.Before(weekStart) {
			summary.PriorWeek.add(row)
		}
		if i, ok := dayIndex[hour.Format("2006-01-02")]; ok {
			summary.Days[i].Observed = true
			summary.Days[i].add(row)
		}
		if !hour.Before(first24) && !hour.After(currentHour) {
			i := int(hour.Sub(first24) / time.Hour)
			if i >= 0 && i < 24 {
				summary.Last24Hours[i].add(row)
			}
		}
	}
	summary.Week.UniqueCallers = report.UniqueCallers
	summary.PriorWeek.UniqueCallers = report.PriorUniqueCallers

	summary.Endpoints = summarizeEndpoints(report)
	summary.Clients, summary.Kinds, summary.Protocols = summarizeClients(report, serverVersion, &summary)
	summary.Countries, summary.CountryTotal, summary.CountryUnknown = summarizeCountries(report)
	return summary
}

func summarizeEndpoints(report Report) []EndpointSummary {
	byRoute := make(map[string]*EndpointSummary)
	histograms := make(map[string]*HourlyCounts)
	for _, row := range report.Routes {
		endpoint := byRoute[row.Route]
		if endpoint == nil {
			endpoint = &EndpointSummary{Route: row.Route}
			byRoute[row.Route] = endpoint
			histograms[row.Route] = &HourlyCounts{}
		}
		endpoint.Calls += row.Requests
		switch row.Outcome {
		case OutcomeSuccess:
			endpoint.Success += row.Requests
		case OutcomeFailure:
			endpoint.Failure += row.Requests
		case OutcomeRejected:
			endpoint.Rejected += row.Requests
		}
		histograms[row.Route].Merge(row.HourlyCounts)
	}
	for _, callers := range report.RouteCallers {
		if endpoint := byRoute[callers.Route]; endpoint != nil {
			endpoint.UniqueCallers = callers.Callers
		}
	}
	out := make([]EndpointSummary, 0, len(byRoute))
	for route, endpoint := range byRoute {
		if endpoint.Calls > 0 {
			endpoint.FailureRate = float64(endpoint.Failure) / float64(endpoint.Calls)
			endpoint.RejectedRate = float64(endpoint.Rejected) / float64(endpoint.Calls)
		}
		histogram := histograms[route]
		endpoint.P95BoundMS, endpoint.P95Over = P95(histogram.Buckets)
		if histogram.Requests > 0 {
			endpoint.MeanMS = histogram.LatencySumMS / histogram.Requests
		}
		out = append(out, *endpoint)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls != out[j].Calls {
			return out[i].Calls > out[j].Calls
		}
		return out[i].Route < out[j].Route
	})
	return out
}

// P95 reads the 95th percentile off a histogram: the bound of the bucket
// in which the cumulative count first reaches 95% of the total. The second
// result is true when that bucket is the overflow bucket.
func P95(buckets [LatencyBuckets]int64) (int64, bool) {
	var total int64
	for _, n := range buckets {
		total += n
	}
	if total == 0 {
		return 0, false
	}
	target := (total*95 + 99) / 100
	var cumulative int64
	for i, n := range buckets {
		cumulative += n
		if cumulative >= target {
			if i == LatencyBuckets-1 {
				return LatencyBoundsMS[len(LatencyBoundsMS)-1], true
			}
			return LatencyBoundsMS[i], false
		}
	}
	return LatencyBoundsMS[len(LatencyBoundsMS)-1], true
}

func summarizeClients(report Report, serverVersion string, summary *Summary) ([]ClientSummary, []KindSummary, []ProtocolSummary) {
	server, serverIsRelease := ParseRelease(serverVersion)
	var total int64
	kinds := make(map[string]int64)
	protocols := make(map[string]int64)
	var mcpTotal int64
	clients := make([]ClientSummary, 0, len(report.Clients))
	for _, row := range report.Clients {
		total += row.Requests
		kinds[row.Kind] += row.Requests
		client := ClientSummary{Kind: row.Kind, Version: row.Version, Protocol: row.Protocol, Requests: row.Requests}
		switch row.Kind {
		case ClientCLI, ClientMCP, ClientDaemon, ClientCSX:
			summary.Identified += row.Requests
			if release, ok := ParseRelease(row.Version); ok && serverIsRelease {
				if release.Less(server) {
					client.Stale = true
					summary.StaleRequests += row.Requests
				} else {
					client.Current = true
					summary.CurrentRequests += row.Requests
				}
			}
		}
		if row.Kind == ClientMCP {
			mcpTotal += row.Requests
			protocols[row.Protocol] += row.Requests
		}
		clients = append(clients, client)
	}
	for i := range clients {
		if total > 0 {
			clients[i].Share = float64(clients[i].Requests) / float64(total)
		}
	}
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Requests != clients[j].Requests {
			return clients[i].Requests > clients[j].Requests
		}
		if clients[i].Kind != clients[j].Kind {
			return clients[i].Kind < clients[j].Kind
		}
		if clients[i].Version != clients[j].Version {
			return clients[i].Version < clients[j].Version
		}
		return clients[i].Protocol < clients[j].Protocol
	})
	kindRows := make([]KindSummary, 0, len(kinds))
	for kind, requests := range kinds {
		row := KindSummary{Kind: kind, Requests: requests}
		if total > 0 {
			row.Share = float64(requests) / float64(total)
		}
		kindRows = append(kindRows, row)
	}
	sort.Slice(kindRows, func(i, j int) bool {
		if kindRows[i].Requests != kindRows[j].Requests {
			return kindRows[i].Requests > kindRows[j].Requests
		}
		return kindRows[i].Kind < kindRows[j].Kind
	})
	protocolRows := make([]ProtocolSummary, 0, len(protocols))
	for protocol, requests := range protocols {
		row := ProtocolSummary{Protocol: protocol, Requests: requests}
		if mcpTotal > 0 {
			row.Share = float64(requests) / float64(mcpTotal)
		}
		protocolRows = append(protocolRows, row)
	}
	sort.Slice(protocolRows, func(i, j int) bool {
		if protocolRows[i].Requests != protocolRows[j].Requests {
			return protocolRows[i].Requests > protocolRows[j].Requests
		}
		return protocolRows[i].Protocol < protocolRows[j].Protocol
	})
	return clients, kindRows, protocolRows
}

func summarizeCountries(report Report) ([]CountrySummary, int64, int64) {
	var total, unknown int64
	byCountry := make(map[string]int64)
	for _, row := range report.Countries {
		total += row.Requests
		if row.Country == "" {
			unknown += row.Requests
			continue
		}
		byCountry[row.Country] += row.Requests
	}
	out := make([]CountrySummary, 0, len(byCountry))
	for country, requests := range byCountry {
		row := CountrySummary{Country: country, Requests: requests}
		if total > 0 {
			row.Share = float64(requests) / float64(total)
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Requests != out[j].Requests {
			return out[i].Requests > out[j].Requests
		}
		return out[i].Country < out[j].Country
	})
	return out, total, unknown
}
