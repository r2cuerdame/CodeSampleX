package admin

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	accessLogWindowDays  = 31
	accessLogCacheTTL    = 5 * time.Minute
	accessLogRolledLimit = 40
	accessLogLineLimit   = 64 << 10
	// Caddy rolls at 10 MiB. The extra line permits the bounded reader to
	// finish the request which crossed the roll boundary without allowing an
	// unexpectedly large file (or gzip expansion) to consume unbounded work.
	accessLogFileLimit = (10 << 20) + accessLogLineLimit
)

// AccessRouteClass is a fixed, privacy-safe API route label. Raw paths and
// path parameters never leave the parser.
type AccessRouteClass string

const (
	AccessRouteSearch           AccessRouteClass = "search"
	AccessRouteEvidence         AccessRouteClass = "evidence"
	AccessRouteRegistry         AccessRouteClass = "registry"
	AccessRouteShards           AccessRouteClass = "shards"
	AccessRouteSamples          AccessRouteClass = "samples"
	AccessRouteWanted           AccessRouteClass = "wanted"
	AccessRouteAdoption         AccessRouteClass = "adoption"
	AccessRouteVerifications    AccessRouteClass = "verifications"
	AccessRouteVerificationJobs AccessRouteClass = "verification_jobs"
	AccessRoutePeers            AccessRouteClass = "peers"
	AccessRouteStats            AccessRouteClass = "stats"
	AccessRouteAdapters         AccessRouteClass = "adapters"
	AccessRouteAuth             AccessRouteClass = "auth"
)

// AccessActivityGroup is a fixed decision-oriented grouping of API routes.
// It describes request activity, never people or sessions.
type AccessActivityGroup string

const (
	AccessGroupConsumption  AccessActivityGroup = "consumption"
	AccessGroupContribution AccessActivityGroup = "contribution"
	AccessGroupCoordination AccessActivityGroup = "coordination"
)

// AccessStatusCounts contains only coarse counters. Status429 is deliberately
// also included in Status4xx: it is a highlighted subset, not a disjoint band.
type AccessStatusCounts struct {
	Requests            int64
	GetHeadRequests     int64
	PostRequests        int64
	OtherMethodRequests int64
	Status2xx           int64
	Status3xx           int64
	Status4xx           int64
	Status429           int64
	Status5xx           int64
	StatusOther         int64
}

// AccessRouteCounts is one fixed route-class row.
type AccessRouteCounts struct {
	Class      AccessRouteClass
	Label      string
	Group      AccessActivityGroup
	GroupLabel string
	AccessStatusCounts
}

// AccessGroupCounts is one of the three fixed activity groups.
type AccessGroupCounts struct {
	Group AccessActivityGroup
	Label string
	AccessStatusCounts
}

// AccessLogDay is one UTC calendar day. HasRequests distinguishes a retained
// day with observed calls from a zero-filled chart position; it does not claim
// that a zero position had complete log coverage.
type AccessLogDay struct {
	Date        string
	HasRequests bool
	AccessStatusCounts
	Routes []AccessRouteCounts
	Groups []AccessGroupCounts
}

// AccessLogMetrics is a bounded 31-day view over the privacy-safe Caddy log.
// CollectionStartedAt comes from a one-time deployment marker when present.
// OldestEventAt and NewestEventAt describe only the retained files actually
// read, so the dashboard can disclose partial coverage honestly.
type AccessLogMetrics struct {
	Days   []AccessLogDay
	Routes []AccessRouteCounts
	Groups []AccessGroupCounts
	Totals AccessStatusCounts

	CollectionStartedAt time.Time
	OldestEventAt       time.Time
	NewestEventAt       time.Time
	DaysWithRequests    int
	SourceFiles         int
	TruncatedFiles      int
	MalformedLines      int64
	OversizeLines       int64
}

// AccessLogReader streams access-safe.log and at most forty Caddy rolls. It
// caches the completed aggregate for five minutes and reuses unchanged rolled
// file aggregates after that, so /admin does not repeatedly parse the full
// retained log set.
type AccessLogReader struct {
	path  string
	clock func() time.Time

	mu         sync.Mutex
	cached     AccessLogMetrics
	cacheDay   string
	cacheUntil time.Time
	fileCache  map[string]cachedAccessFile
}

type cachedAccessFile struct {
	signature accessFileSignature
	windowDay string
	aggregate accessFileAggregate
}

type accessFileSignature struct {
	size    int64
	modNano int64
}

type accessFileAggregate struct {
	days           map[string]*accessDayAggregate
	oldest         time.Time
	newest         time.Time
	malformedLines int64
	oversizeLines  int64
	truncated      bool
}

type accessDayAggregate struct {
	total  AccessStatusCounts
	routes [len(accessRouteDefinitions)]AccessStatusCounts
	groups [len(accessGroupDefinitions)]AccessStatusCounts
}

type accessRouteDefinition struct {
	class      AccessRouteClass
	label      string
	group      int
	mixedLabel string
}

var accessRouteDefinitions = [...]accessRouteDefinition{
	{class: AccessRouteSearch, label: "검색", group: 0},
	{class: AccessRouteSamples, label: "샘플", group: -1, mixedLabel: "조회·게시 혼합"},
	{class: AccessRouteShards, label: "샤드", group: 0},
	{class: AccessRouteWanted, label: "요청 대기열", group: -1, mixedLabel: "조회·보고 혼합"},
	{class: AccessRouteAdoption, label: "채택 보고", group: -1, mixedLabel: "POST 기여 · 기타 조정"},
	{class: AccessRouteEvidence, label: "증거 제출", group: -1, mixedLabel: "POST 기여 · 기타 조정"},
	{class: AccessRouteVerifications, label: "검증 결과 제출", group: -1, mixedLabel: "POST 기여 · 기타 조정"},
	{class: AccessRouteVerificationJobs, label: "검증 작업", group: 2},
	{class: AccessRouteStats, label: "통계", group: 2},
	{class: AccessRoutePeers, label: "피어 조정", group: 2},
	{class: AccessRouteRegistry, label: "레지스트리", group: 0},
	{class: AccessRouteAdapters, label: "어댑터", group: 2},
	{class: AccessRouteAuth, label: "인증", group: 2},
}

type accessGroupDefinition struct {
	group AccessActivityGroup
	label string
}

var accessGroupDefinitions = [...]accessGroupDefinition{
	{AccessGroupConsumption, "검색·전달"},
	{AccessGroupContribution, "피드백·기여"},
	{AccessGroupCoordination, "조정·메타데이터"},
}

var (
	errUnsafeAccessLogPath = errors.New("admin access log path must end in access-safe.log")
	errAccessLogDirectory  = errors.New("admin access log directory is unavailable")
	errAccessLogRead       = errors.New("admin access log could not be read")
	errAccessLogGzip       = errors.New("admin access log gzip stream is invalid")
)

// NewAccessLogReader constructs the bounded reader. Metrics rejects every
// basename except access-safe.log, preventing an accidental fallback to the
// older logs which can contain query strings.
func NewAccessLogReader(path string) *AccessLogReader {
	return &AccessLogReader{
		path:      path,
		clock:     time.Now,
		fileCache: make(map[string]cachedAccessFile),
	}
}

// Metrics returns the last 31 UTC calendar days, including the day containing
// now. A missing safe log is a normal empty state while Caddy starts; real
// permission, gzip and read failures are returned as generic errors which do
// not contain a raw line or URI.
func (r *AccessLogReader) Metrics(ctx context.Context, now time.Time) (AccessLogMetrics, error) {
	if r == nil || filepath.Base(r.path) != "access-safe.log" {
		return AccessLogMetrics{}, errUnsafeAccessLogPath
	}
	if now.IsZero() {
		now = r.clock()
	}
	now = now.UTC()
	dayKey := now.Format("2006-01-02")

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return AccessLogMetrics{}, err
	}
	clockNow := r.clock()
	if r.cacheDay == dayKey && clockNow.Before(r.cacheUntil) {
		return cloneAccessLogMetrics(r.cached), nil
	}

	files, err := selectAccessLogFiles(r.path)
	if err != nil {
		return AccessLogMetrics{}, err
	}
	nextFileCache := make(map[string]cachedAccessFile, len(files))
	aggregates := make([]accessFileAggregate, 0, len(files))
	windowStart := accessUTCDay(now).AddDate(0, 0, -(accessLogWindowDays - 1))
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return AccessLogMetrics{}, err
		}
		cached, ok := r.fileCache[file.path]
		if ok && cached.signature == file.signature && cached.windowDay == dayKey {
			nextFileCache[file.path] = cached
			aggregates = append(aggregates, cached.aggregate)
			continue
		}
		aggregate, err := parseAccessLogFile(ctx, file, windowStart, now)
		if err != nil {
			// A roll may disappear between ReadDir and Open. It will either be
			// represented by its replacement or picked up on the next refresh.
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return AccessLogMetrics{}, err
		}
		cached = cachedAccessFile{signature: file.signature, windowDay: dayKey, aggregate: aggregate}
		// Preserve completed fixed-counter work even if a later file reaches
		// the request deadline. The next authenticated request can resume from
		// this file boundary instead of starting the retained corpus over.
		r.fileCache[file.path] = cached
		nextFileCache[file.path] = cached
		aggregates = append(aggregates, aggregate)
	}

	metrics := combineAccessAggregates(now, aggregates)
	metrics.SourceFiles = len(aggregates)
	metrics.CollectionStartedAt = readCollectionStart(r.path + ".since")
	if metrics.CollectionStartedAt.IsZero() {
		metrics.CollectionStartedAt = metrics.OldestEventAt
	}
	r.fileCache = nextFileCache
	r.cached = cloneAccessLogMetrics(metrics)
	r.cacheDay = dayKey
	r.cacheUntil = clockNow.Add(accessLogCacheTTL)
	return cloneAccessLogMetrics(metrics), nil
}

type selectedAccessFile struct {
	path       string
	compressed bool
	signature  accessFileSignature
	modTime    time.Time
}

func selectAccessLogFiles(base string) ([]selectedAccessFile, error) {
	directory := filepath.Dir(base)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errAccessLogDirectory
	}

	baseName := filepath.Base(base)
	rolledPrefix := strings.TrimSuffix(baseName, filepath.Ext(baseName)) + "-"
	var current *selectedAccessFile
	rolled := make([]selectedAccessFile, 0, accessLogRolledLimit)
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		isCurrent := name == baseName
		isRolled := strings.HasPrefix(name, rolledPrefix) &&
			(strings.HasSuffix(name, ".log") || strings.HasSuffix(name, ".log.gz"))
		if !isCurrent && !isRolled {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, errAccessLogDirectory
		}
		if !info.Mode().IsRegular() {
			continue
		}
		selected := selectedAccessFile{
			path:       filepath.Join(directory, name),
			compressed: strings.HasSuffix(name, ".gz"),
			signature:  accessFileSignature{size: info.Size(), modNano: info.ModTime().UnixNano()},
			modTime:    info.ModTime(),
		}
		if isCurrent {
			copy := selected
			current = &copy
		} else {
			rolled = append(rolled, selected)
		}
	}

	// Keep only the newest bounded roll set, then read it oldest-first. The
	// aggregate is commutative, but deterministic order makes tests and
	// coverage timestamps easier to audit.
	sort.Slice(rolled, func(i, j int) bool {
		if rolled[i].modTime.Equal(rolled[j].modTime) {
			return rolled[i].path > rolled[j].path
		}
		return rolled[i].modTime.After(rolled[j].modTime)
	})
	if len(rolled) > accessLogRolledLimit {
		rolled = rolled[:accessLogRolledLimit]
	}
	sort.Slice(rolled, func(i, j int) bool {
		if rolled[i].modTime.Equal(rolled[j].modTime) {
			return rolled[i].path < rolled[j].path
		}
		return rolled[i].modTime.Before(rolled[j].modTime)
	})
	files := rolled
	if current != nil {
		files = append(files, *current)
	}
	return files, nil
}

func parseAccessLogFile(ctx context.Context, file selectedAccessFile, windowStart, windowEnd time.Time) (accessFileAggregate, error) {
	f, err := os.Open(file.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return accessFileAggregate{}, os.ErrNotExist
		}
		return accessFileAggregate{}, errAccessLogRead
	}
	defer f.Close()

	var source io.Reader = f
	if file.compressed {
		compressed, err := gzip.NewReader(f)
		if err != nil {
			return accessFileAggregate{}, errAccessLogGzip
		}
		defer compressed.Close()
		source = compressed
	}
	limited := &io.LimitedReader{R: source, N: accessLogFileLimit + 1}
	aggregate, err := parseAccessLog(ctx, limited, windowStart, windowEnd)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return accessFileAggregate{}, err
		}
		if file.compressed {
			return accessFileAggregate{}, errAccessLogGzip
		}
		return accessFileAggregate{}, errAccessLogRead
	}
	if limited.N == 0 {
		aggregate.truncated = true
	}
	return aggregate, nil
}

func parseAccessLog(ctx context.Context, source io.Reader, windowStart, windowEnd time.Time) (accessFileAggregate, error) {
	aggregate := accessFileAggregate{days: make(map[string]*accessDayAggregate)}
	reader := bufio.NewReaderSize(source, 32<<10)
	for {
		line, oversized, err := readBoundedAccessLine(reader)
		if oversized {
			aggregate.oversizeLines++
		} else if len(line) != 0 {
			if !aggregateAccessLine(line, windowStart, windowEnd, &aggregate) {
				aggregate.malformedLines++
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return aggregate, nil
			}
			return accessFileAggregate{}, err
		}
		select {
		case <-ctx.Done():
			return accessFileAggregate{}, ctx.Err()
		default:
		}
	}
}

func readBoundedAccessLine(reader *bufio.Reader) ([]byte, bool, error) {
	line := make([]byte, 0, 512)
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > accessLogLineLimit {
				line = nil
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}
		switch {
		case err == nil:
			return trimAccessLine(line), oversized, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			return trimAccessLine(line), oversized, io.EOF
		default:
			return nil, oversized, err
		}
	}
}

func trimAccessLine(line []byte) []byte {
	return bytes.TrimSpace(line)
}

type caddyAccessEntry struct {
	Timestamp json.Number `json:"ts"`
	Status    int         `json:"status"`
	Route     string      `json:"csx_route"`
	Method    string      `json:"csx_method"`
}

func aggregateAccessLine(line []byte, windowStart, windowEnd time.Time, aggregate *accessFileAggregate) bool {
	var entry caddyAccessEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return false
	}
	when, ok := parseAccessTimestamp(entry.Timestamp.String())
	if !ok || entry.Status < 100 || entry.Status > 599 {
		return false
	}
	if when.Before(windowStart) || when.After(windowEnd) {
		return true
	}
	route, ok := classifyAccessRoute(entry.Route)
	if !ok {
		// Non-API requests (including /healthz) are intentionally ignored.
		return true
	}
	dayKey := when.Format("2006-01-02")
	day := aggregate.days[dayKey]
	if day == nil {
		day = &accessDayAggregate{}
		aggregate.days[dayKey] = day
	}
	group := accessGroupForRequest(route, entry.Method)
	addAccessCount(&day.total, entry.Method, entry.Status)
	addAccessCount(&day.routes[route], entry.Method, entry.Status)
	addAccessCount(&day.groups[group], entry.Method, entry.Status)
	if aggregate.oldest.IsZero() || when.Before(aggregate.oldest) {
		aggregate.oldest = when
	}
	if aggregate.newest.IsZero() || when.After(aggregate.newest) {
		aggregate.newest = when
	}
	return true
}

func parseAccessTimestamp(raw string) (time.Time, bool) {
	seconds, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds < 0 || seconds > 253402300799 {
		return time.Time{}, false
	}
	whole, fraction := math.Modf(seconds)
	return time.Unix(int64(whole), int64(fraction*float64(time.Second))).UTC(), true
}

func classifyAccessRoute(label string) (int, bool) {
	// csx_route is appended by one of Caddy's allowlisted path matchers while
	// the URI itself is deleted. Accept only these exact constants: even a
	// malformed or forged log line cannot turn raw input into an output label.
	switch label {
	case "search":
		return accessRouteIndex(AccessRouteSearch), true
	case "evidence":
		return accessRouteIndex(AccessRouteEvidence), true
	case "registry":
		return accessRouteIndex(AccessRouteRegistry), true
	case "shards":
		return accessRouteIndex(AccessRouteShards), true
	case "samples":
		return accessRouteIndex(AccessRouteSamples), true
	case "wanted":
		return accessRouteIndex(AccessRouteWanted), true
	case "adoption":
		return accessRouteIndex(AccessRouteAdoption), true
	case "verifications":
		return accessRouteIndex(AccessRouteVerifications), true
	case "verification_jobs":
		return accessRouteIndex(AccessRouteVerificationJobs), true
	case "peers":
		return accessRouteIndex(AccessRoutePeers), true
	case "stats":
		return accessRouteIndex(AccessRouteStats), true
	case "adapters":
		return accessRouteIndex(AccessRouteAdapters), true
	case "auth":
		return accessRouteIndex(AccessRouteAuth), true
	default:
		return 0, false
	}
}

func accessRouteIndex(class AccessRouteClass) int {
	for i := range accessRouteDefinitions {
		if accessRouteDefinitions[i].class == class {
			return i
		}
	}
	panic("unknown fixed access route class")
}

func accessGroupForRequest(route int, methodBucket string) int {
	definition := accessRouteDefinitions[route]
	switch definition.class {
	case AccessRouteAdoption, AccessRouteEvidence, AccessRouteVerifications:
		if methodBucket == "post" {
			return 1
		}
		// A bot or malformed client GETting a submission endpoint did not
		// contribute data, even if the route family itself is contribution-
		// oriented. Keep those method errors in coordination activity.
		return 2
	case AccessRouteSamples:
		if methodBucket == "post" {
			return 1
		}
		if methodBucket == "get_head" {
			return 0
		}
		return 2
	case AccessRouteWanted:
		if methodBucket == "post" {
			return 1
		}
		return 2
	default:
		if definition.group >= 0 {
			return definition.group
		}
		panic("fixed access route is missing an activity group")
	}
}

func addAccessCount(counts *AccessStatusCounts, methodBucket string, status int) {
	counts.Requests++
	switch methodBucket {
	case "get_head":
		counts.GetHeadRequests++
	case "post":
		counts.PostRequests++
	default:
		counts.OtherMethodRequests++
	}
	switch {
	case status >= 200 && status <= 299:
		counts.Status2xx++
	case status >= 300 && status <= 399:
		counts.Status3xx++
	case status >= 400 && status <= 499:
		counts.Status4xx++
		if status == 429 {
			counts.Status429++
		}
	case status >= 500 && status <= 599:
		counts.Status5xx++
	default:
		counts.StatusOther++
	}
}

func combineAccessAggregates(now time.Time, files []accessFileAggregate) AccessLogMetrics {
	start := accessUTCDay(now).AddDate(0, 0, -(accessLogWindowDays - 1))
	metrics := AccessLogMetrics{
		Days:   make([]AccessLogDay, accessLogWindowDays),
		Routes: emptyAccessRouteCounts(),
		Groups: emptyAccessGroupCounts(),
	}
	dayIndexes := make(map[string]int, accessLogWindowDays)
	for i := 0; i < accessLogWindowDays; i++ {
		day := start.AddDate(0, 0, i)
		date := day.Format("2006-01-02")
		metrics.Days[i] = AccessLogDay{
			Date:   date,
			Routes: emptyAccessRouteCounts(),
			Groups: emptyAccessGroupCounts(),
		}
		dayIndexes[date] = i
	}
	for _, file := range files {
		metrics.MalformedLines += file.malformedLines
		metrics.OversizeLines += file.oversizeLines
		if file.truncated {
			metrics.TruncatedFiles++
		}
		if !file.oldest.IsZero() && (metrics.OldestEventAt.IsZero() || file.oldest.Before(metrics.OldestEventAt)) {
			metrics.OldestEventAt = file.oldest
		}
		if !file.newest.IsZero() && (metrics.NewestEventAt.IsZero() || file.newest.After(metrics.NewestEventAt)) {
			metrics.NewestEventAt = file.newest
		}
		for date, aggregate := range file.days {
			index, ok := dayIndexes[date]
			if !ok {
				continue
			}
			day := &metrics.Days[index]
			mergeAccessCounts(&day.AccessStatusCounts, aggregate.total)
			for i := range aggregate.routes {
				mergeAccessCounts(&day.Routes[i].AccessStatusCounts, aggregate.routes[i])
				mergeAccessCounts(&metrics.Routes[i].AccessStatusCounts, aggregate.routes[i])
			}
			for i := range aggregate.groups {
				mergeAccessCounts(&day.Groups[i].AccessStatusCounts, aggregate.groups[i])
				mergeAccessCounts(&metrics.Groups[i].AccessStatusCounts, aggregate.groups[i])
			}
			mergeAccessCounts(&metrics.Totals, aggregate.total)
		}
	}
	for i := range metrics.Days {
		metrics.Days[i].HasRequests = metrics.Days[i].Requests > 0
		if metrics.Days[i].HasRequests {
			metrics.DaysWithRequests++
		}
	}
	return metrics
}

func accessUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func emptyAccessRouteCounts() []AccessRouteCounts {
	rows := make([]AccessRouteCounts, len(accessRouteDefinitions))
	for i, definition := range accessRouteDefinitions {
		rows[i] = AccessRouteCounts{Class: definition.class, Label: definition.label}
		if definition.group >= 0 {
			rows[i].Group = accessGroupDefinitions[definition.group].group
			rows[i].GroupLabel = accessGroupDefinitions[definition.group].label
		} else {
			rows[i].GroupLabel = definition.mixedLabel
		}
	}
	return rows
}

func emptyAccessGroupCounts() []AccessGroupCounts {
	rows := make([]AccessGroupCounts, len(accessGroupDefinitions))
	for i, definition := range accessGroupDefinitions {
		rows[i] = AccessGroupCounts{Group: definition.group, Label: definition.label}
	}
	return rows
}

func mergeAccessCounts(target *AccessStatusCounts, source AccessStatusCounts) {
	target.Requests += source.Requests
	target.GetHeadRequests += source.GetHeadRequests
	target.PostRequests += source.PostRequests
	target.OtherMethodRequests += source.OtherMethodRequests
	target.Status2xx += source.Status2xx
	target.Status3xx += source.Status3xx
	target.Status4xx += source.Status4xx
	target.Status429 += source.Status429
	target.Status5xx += source.Status5xx
	target.StatusOther += source.StatusOther
}

func readCollectionStart(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return time.Time{}
	}
	started, err := time.Parse(time.RFC3339, strings.TrimSpace(string(raw)))
	if err != nil {
		return time.Time{}
	}
	return started.UTC()
}

func cloneAccessLogMetrics(source AccessLogMetrics) AccessLogMetrics {
	clone := source
	clone.Routes = append([]AccessRouteCounts(nil), source.Routes...)
	clone.Groups = append([]AccessGroupCounts(nil), source.Groups...)
	clone.Days = make([]AccessLogDay, len(source.Days))
	for i, day := range source.Days {
		clone.Days[i] = day
		clone.Days[i].Routes = append([]AccessRouteCounts(nil), day.Routes...)
		clone.Days[i].Groups = append([]AccessGroupCounts(nil), day.Groups...)
	}
	return clone
}
