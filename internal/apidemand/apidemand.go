// Package apidemand records bounded, privacy-safe API demand telemetry for
// the private operator dashboard: how many calls each fixed route received,
// how they ended, how long they took, which client build made them and --
// only when the edge supplies it -- which country they came from.
//
// What is stored is deliberately coarse. Every dimension is a server-chosen
// constant or a value validated against a fixed grammar before it is kept:
// the route is the registered mux pattern (never the request path), the
// outcome is one of three classes, the country is a two-letter code from a
// trusted edge header, the client is a parsed csx build token. There is no
// raw address, no User-Agent string, no query, no path parameter and no
// timestamp finer than an hour. Callers are counted through the same
// pseudonymous hash the anonymous-client ledger already keeps, and an
// authenticated caller through a keyed digest of its credential; neither can
// be turned back into the credential.
package apidemand

import (
	"context"
	"time"
)

// Outcome classes. Success is any 2xx or 3xx answer including a conditional
// 304; Rejected is a 4xx the server refused (auth, rate limit, malformed
// body); Failure is a 5xx the server could not answer.
const (
	OutcomeSuccess  = "success"
	OutcomeRejected = "rejected"
	OutcomeFailure  = "failure"
)

// Auth classes. Authenticated carries an Authorization header; Anonymous
// carries a valid pseudonymous client id (header or cookie); Unidentified is
// everything else, typically a first-contact request the server answers by
// issuing an id.
const (
	AuthAuthenticated = "authenticated"
	AuthAnonymous     = "anonymous"
	AuthUnidentified  = "unidentified"
)

// Client kinds. The csx surfaces identify themselves in a fixed User-Agent
// grammar; anything else collapses to Other (a non-empty foreign agent) or
// None (no User-Agent at all). The raw User-Agent is never stored.
const (
	ClientCLI    = "cli"
	ClientMCP    = "mcp"
	ClientDaemon = "daemon"
	ClientCSX    = "csx"
	ClientOther  = "other"
	ClientNone   = "none"
)

// LatencyBoundsMS are the inclusive upper bounds of the fixed latency
// histogram in milliseconds. The final bucket holds everything slower than
// the last bound. p95 is read off this histogram, so it is a bucket bound,
// never an exact millisecond.
var LatencyBoundsMS = [...]int64{10, 25, 50, 100, 250, 500, 1000, 2500, 5000}

// LatencyBuckets is the histogram width: one bucket per bound plus overflow.
const LatencyBuckets = len(LatencyBoundsMS) + 1

// Observation is one classified request. It is produced on the request path
// and aggregated off it; nothing here is written per request.
type Observation struct {
	At            time.Time
	Route         string
	Outcome       string
	Auth          string
	CallerHash    string
	Country       string
	ClientKind    string
	ClientVersion string
	Protocol      string
	LatencyMS     int64
}

// HourlyKey identifies one aggregate row: a UTC hour, a fixed route and the
// two outcome dimensions.
type HourlyKey struct {
	Hour    time.Time
	Route   string
	Outcome string
	Auth    string
}

// HourlyCounts is the aggregate carried by one HourlyKey.
type HourlyCounts struct {
	Requests     int64
	LatencySumMS int64
	Buckets      [LatencyBuckets]int64
}

// Add folds one observation into the counts.
func (c *HourlyCounts) Add(latencyMS int64) {
	c.Requests++
	if latencyMS < 0 {
		latencyMS = 0
	}
	c.LatencySumMS += latencyMS
	c.Buckets[LatencyBucket(latencyMS)]++
}

// Merge adds another aggregate into this one.
func (c *HourlyCounts) Merge(o HourlyCounts) {
	c.Requests += o.Requests
	c.LatencySumMS += o.LatencySumMS
	for i := range c.Buckets {
		c.Buckets[i] += o.Buckets[i]
	}
}

// LatencyBucket returns the histogram index for a latency.
func LatencyBucket(latencyMS int64) int {
	for i, bound := range LatencyBoundsMS {
		if latencyMS <= bound {
			return i
		}
	}
	return LatencyBuckets - 1
}

// ClientKey identifies one client build on one UTC day.
type ClientKey struct {
	Day      string
	Kind     string
	Version  string
	Protocol string
}

// CountryKey identifies one edge-reported country on one UTC day. An empty
// Country is "the edge did not say", which is a real bucket, not a zero.
type CountryKey struct {
	Day     string
	Country string
}

// CallerKey identifies one pseudonymous caller on one route on one UTC day.
// It exists so unique callers can be counted; it is pruned with the rest.
type CallerKey struct {
	Day        string
	Route      string
	CallerHash string
}

// Batch is one flush: everything observed since the previous flush, already
// aggregated so the store performs one upsert per distinct key.
type Batch struct {
	Hourly    map[HourlyKey]*HourlyCounts
	Clients   map[ClientKey]int64
	Countries map[CountryKey]int64
	Callers   map[CallerKey]int64
}

// NewBatch returns an empty batch.
func NewBatch() Batch {
	return Batch{
		Hourly:    map[HourlyKey]*HourlyCounts{},
		Clients:   map[ClientKey]int64{},
		Countries: map[CountryKey]int64{},
		Callers:   map[CallerKey]int64{},
	}
}

// Empty reports whether nothing was observed.
func (b Batch) Empty() bool {
	return len(b.Hourly) == 0 && len(b.Clients) == 0 && len(b.Countries) == 0 && len(b.Callers) == 0
}

// Add folds one observation into the batch.
func (b Batch) Add(o Observation) {
	at := o.At.UTC()
	hour := at.Truncate(time.Hour)
	day := at.Format("2006-01-02")
	key := HourlyKey{Hour: hour, Route: o.Route, Outcome: o.Outcome, Auth: o.Auth}
	counts := b.Hourly[key]
	if counts == nil {
		counts = &HourlyCounts{}
		b.Hourly[key] = counts
	}
	counts.Add(o.LatencyMS)
	b.Clients[ClientKey{Day: day, Kind: o.ClientKind, Version: o.ClientVersion, Protocol: o.Protocol}]++
	b.Countries[CountryKey{Day: day, Country: o.Country}]++
	if o.CallerHash != "" {
		b.Callers[CallerKey{Day: day, Route: o.Route, CallerHash: o.CallerHash}]++
	}
}

// Store persists batches and answers the bounded report. The zero-day
// retention is the store's contract too: PruneDemand removes every row whose
// hour or day is before the cutoff.
type Store interface {
	UpsertDemand(ctx context.Context, batch Batch) error
	PruneDemand(ctx context.Context, before time.Time) error
	DemandReport(ctx context.Context, now time.Time) (Report, error)
}

// Report is the bounded read the dashboard consumes: fourteen days of hourly
// outcome rows, seven days of per-route latency and caller counts, and the
// seven-day client and country splits. Nothing in it is per request.
type Report struct {
	// Hours covers [now-14d, now] with one row per (hour, outcome, auth).
	Hours []HourRow
	// Routes covers [now-7d, now] with one row per (route, outcome).
	Routes []RouteRow
	// RouteCallers is the distinct caller count per route over the same
	// seven days.
	RouteCallers []RouteCallers
	// UniqueCallers is the distinct caller count over the last seven days;
	// PriorUniqueCallers over the seven before that.
	UniqueCallers      int64
	PriorUniqueCallers int64
	// Clients and Countries cover the last seven UTC days.
	Clients   []ClientRow
	Countries []CountryRow
	// OldestHour is the earliest retained hour, so a dashboard can say when
	// collection began instead of reading a short history as low demand.
	OldestHour time.Time
}

// HourRow is one (hour, outcome, auth) aggregate.
type HourRow struct {
	Hour     time.Time
	Outcome  string
	Auth     string
	Requests int64
}

// RouteRow is one (route, outcome) aggregate with its latency histogram.
type RouteRow struct {
	Route   string
	Outcome string
	HourlyCounts
}

// RouteCallers is the distinct pseudonymous caller count for one route.
type RouteCallers struct {
	Route   string
	Callers int64
}

// ClientRow is one client build's request count.
type ClientRow struct {
	Kind     string
	Version  string
	Protocol string
	Requests int64
}

// CountryRow is one edge-reported country's request count.
type CountryRow struct {
	Country  string
	Requests int64
}
