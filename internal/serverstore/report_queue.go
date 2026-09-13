package serverstore

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReportQueueStore is a private operations projection. Lists omit submitted
// evidence and anonymous reporter identifiers; evidence is fetched on demand.
type ReportQueueStore interface {
	ReportQueue(context.Context, ReportFilter) (ReportPage, error)
	ReviewProductReport(context.Context, int64, string, string, string, time.Time) (bool, error)
	ProductReviewNote(context.Context, int64) (string, error)
}

type ReportFilter struct {
	Channel, State, Query string
	Offset, Limit         int
}
type ReportSummary struct {
	ID           int64     `json:"id"`
	Channel      string    `json:"channel"`
	Kind         string    `json:"kind"`
	Target       string    `json:"target"`
	Status       string    `json:"status"`
	Verdict      string    `json:"verdict"`
	Reason       string    `json:"reason"`
	CanonicalRef string    `json:"canonicalRef"`
	Occurrences  int64     `json:"occurrences"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
}
type ReportCounts struct {
	Total    int `json:"total"`
	Open     int `json:"open"`
	Blocked  int `json:"blocked"`
	Resolved int `json:"resolved"`
	Unlinked int `json:"unlinked"`
}
type ReportPage struct {
	Rows   []ReportSummary `json:"rows"`
	Total  int             `json:"total"`
	Counts ReportCounts    `json:"counts"`
}

func (f ReportFilter) normalized() ReportFilter {
	if f.Limit < 1 || f.Limit > 100 {
		f.Limit = 25
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	f.Query = strings.ToLower(strings.TrimSpace(f.Query))
	return f
}
func reportBlocked(r ReportSummary) bool {
	return r.Verdict == "" && (r.Status == "unsupported" || r.Status == "no-replay-lane")
}
func reportUnlinked(r ReportSummary) bool {
	return r.Verdict == "confirmed-csx-defect" && r.CanonicalRef == "" && r.Channel == "product"
}
func productSummary(r CSXIssueReportRow) ReportSummary {
	return ReportSummary{r.ID, "product", r.IssueKind, r.Surface + " · " + r.Component, r.Status, r.Verdict, r.ReplayReason, r.CanonicalRef, r.Occurrences, r.FirstSeen, r.LastSeen}
}
func anomalySummary(r AnomalyReportRow) ReportSummary {
	return ReportSummary{r.ID, "anomaly", r.AnomalyType, r.PURL + " · " + r.Symbol, r.Status, r.Verdict, r.UnsupportedReason, "", r.Reports, r.FirstSeen, r.LastSeen}
}

func (f *Fake) ReportQueue(_ context.Context, filter ReportFilter) (ReportPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter = filter.normalized()
	all := []ReportSummary{}
	for _, r := range f.csxIssues {
		all = append(all, productSummary(*r))
	}
	for _, r := range f.anomalies {
		all = append(all, anomalySummary(*r))
	}
	out := ReportPage{Rows: []ReportSummary{}}
	matched := []ReportSummary{}
	for _, r := range all {
		if filter.Channel != "all" && r.Channel != filter.Channel {
			continue
		}
		out.Counts.Total++
		if r.Verdict == "" {
			out.Counts.Open++
		} else {
			out.Counts.Resolved++
		}
		if reportBlocked(r) {
			out.Counts.Blocked++
		}
		if reportUnlinked(r) {
			out.Counts.Unlinked++
		}
		if filter.State == "open" && r.Verdict != "" || filter.State == "resolved" && r.Verdict == "" || filter.State == "blocked" && !reportBlocked(r) || filter.State == "unlinked" && !reportUnlinked(r) {
			continue
		}
		search := strings.ToLower(strconv.FormatInt(r.ID, 10) + " " + r.Kind + " " + r.Target + " " + r.Status + " " + r.Verdict + " " + r.Reason + " " + r.CanonicalRef)
		if !strings.Contains(search, filter.Query) {
			continue
		}
		matched = append(matched, r)
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].LastSeen.Equal(matched[j].LastSeen) {
			if matched[i].ID == matched[j].ID {
				return matched[i].Channel < matched[j].Channel
			}
			return matched[i].ID > matched[j].ID
		}
		return matched[i].LastSeen.After(matched[j].LastSeen)
	})
	out.Total = len(matched)
	if filter.Offset < len(matched) {
		out.Rows = matched[filter.Offset:min(len(matched), filter.Offset+filter.Limit)]
	}
	return out, nil
}

func (f *Fake) ReviewProductReport(_ context.Context, id int64, verdict, note, ref string, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.findCSXIssueLocked(id)
	if r == nil || r.Verdict != "" {
		return false, nil
	}
	r.Verdict, r.ReviewNote, r.CanonicalRef, r.VerdictAt, r.Status = verdict, note, ref, at, "resolved"
	return true, nil
}
func (f *Fake) ProductReviewNote(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.findCSXIssueLocked(id); r != nil {
		return r.ReviewNote, nil
	}
	return "", nil
}

// Avoid leaking invalid legacy JSON into an otherwise usable detail response.
func ReportEvidence(raw string) json.RawMessage {
	if !json.Valid([]byte(raw)) {
		return json.RawMessage(`null`)
	}
	return json.RawMessage(raw)
}
