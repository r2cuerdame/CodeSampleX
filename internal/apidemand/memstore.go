package apidemand

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store used by tests and the serverstore
// fake. Its report must agree with the PostgreSQL store's row for row; the
// parity test in serverstore plays one script through both.
type MemoryStore struct {
	mu        sync.Mutex
	hourly    map[HourlyKey]*HourlyCounts
	clients   map[ClientKey]int64
	countries map[CountryKey]int64
	callers   map[CallerKey]int64
	// FailWrites makes every write fail, for telemetry tests.
	FailWrites bool
}

func (m *MemoryStore) init() {
	if m.hourly == nil {
		m.hourly = map[HourlyKey]*HourlyCounts{}
		m.clients = map[ClientKey]int64{}
		m.countries = map[CountryKey]int64{}
		m.callers = map[CallerKey]int64{}
	}
}

// UpsertDemand implements Store.
func (m *MemoryStore) UpsertDemand(_ context.Context, batch Batch) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailWrites {
		return errStoreUnavailable
	}
	m.init()
	for key, counts := range batch.Hourly {
		if !ValidRoute(key.Route) {
			continue
		}
		key.Hour = key.Hour.UTC().Truncate(time.Hour)
		existing := m.hourly[key]
		if existing == nil {
			existing = &HourlyCounts{}
			m.hourly[key] = existing
		}
		existing.Merge(*counts)
	}
	for key, n := range batch.Clients {
		m.clients[key] += n
	}
	for key, n := range batch.Countries {
		m.countries[key] += n
	}
	for key, n := range batch.Callers {
		if !ValidRoute(key.Route) {
			continue
		}
		m.callers[key] += n
	}
	return nil
}

// PruneDemand implements Store.
func (m *MemoryStore) PruneDemand(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailWrites {
		return errStoreUnavailable
	}
	m.init()
	cutoffDay := before.UTC().Format("2006-01-02")
	for key := range m.hourly {
		if key.Hour.Before(before) {
			delete(m.hourly, key)
		}
	}
	for key := range m.clients {
		if key.Day < cutoffDay {
			delete(m.clients, key)
		}
	}
	for key := range m.countries {
		if key.Day < cutoffDay {
			delete(m.countries, key)
		}
	}
	for key := range m.callers {
		if key.Day < cutoffDay {
			delete(m.callers, key)
		}
	}
	return nil
}

// DemandReport implements Store with the same windows the PostgreSQL store
// uses: hours over fourteen rolling days, routes and callers over seven
// rolling days, clients and countries over the last seven UTC calendar days.
func (m *MemoryStore) DemandReport(_ context.Context, now time.Time) (Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	now = now.UTC()
	weekStart := now.Add(-7 * 24 * time.Hour)
	priorStart := now.Add(-14 * 24 * time.Hour)
	firstDay := ReportFirstDay(now)
	var report Report

	for key, counts := range m.hourly {
		if report.OldestHour.IsZero() || key.Hour.Before(report.OldestHour) {
			report.OldestHour = key.Hour
		}
		if key.Hour.Before(priorStart) {
			continue
		}
		report.Hours = append(report.Hours, HourRow{Hour: key.Hour, Outcome: key.Outcome, Auth: key.Auth, Requests: counts.Requests})
		if key.Hour.Before(weekStart) {
			continue
		}
		routeKey := RouteRow{Route: key.Route, Outcome: key.Outcome}
		found := false
		for i := range report.Routes {
			if report.Routes[i].Route == routeKey.Route && report.Routes[i].Outcome == routeKey.Outcome {
				report.Routes[i].Merge(*counts)
				found = true
				break
			}
		}
		if !found {
			routeKey.HourlyCounts = *counts
			report.Routes = append(report.Routes, routeKey)
		}
	}
	sort.Slice(report.Hours, func(i, j int) bool {
		a, b := report.Hours[i], report.Hours[j]
		if !a.Hour.Equal(b.Hour) {
			return a.Hour.Before(b.Hour)
		}
		if a.Outcome != b.Outcome {
			return a.Outcome < b.Outcome
		}
		return a.Auth < b.Auth
	})
	sort.Slice(report.Routes, func(i, j int) bool {
		a, b := report.Routes[i], report.Routes[j]
		if a.Route != b.Route {
			return a.Route < b.Route
		}
		return a.Outcome < b.Outcome
	})

	weekDay := weekStart.Format("2006-01-02")
	priorDay := priorStart.Format("2006-01-02")
	perRoute := make(map[string]map[string]bool)
	week := make(map[string]bool)
	prior := make(map[string]bool)
	for key := range m.callers {
		switch {
		case key.Day >= weekDay:
			week[key.CallerHash] = true
			if perRoute[key.Route] == nil {
				perRoute[key.Route] = map[string]bool{}
			}
			perRoute[key.Route][key.CallerHash] = true
		case key.Day >= priorDay:
			prior[key.CallerHash] = true
		}
	}
	report.UniqueCallers = int64(len(week))
	report.PriorUniqueCallers = int64(len(prior))
	for route, hashes := range perRoute {
		report.RouteCallers = append(report.RouteCallers, RouteCallers{Route: route, Callers: int64(len(hashes))})
	}
	sort.Slice(report.RouteCallers, func(i, j int) bool { return report.RouteCallers[i].Route < report.RouteCallers[j].Route })

	clients := make(map[ClientRow]int64)
	for key, n := range m.clients {
		if key.Day < firstDay {
			continue
		}
		clients[ClientRow{Kind: key.Kind, Version: key.Version, Protocol: key.Protocol}] += n
	}
	for row, n := range clients {
		row.Requests = n
		report.Clients = append(report.Clients, row)
	}
	sort.Slice(report.Clients, func(i, j int) bool {
		a, b := report.Clients[i], report.Clients[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		return a.Protocol < b.Protocol
	})
	countries := make(map[string]int64)
	for key, n := range m.countries {
		if key.Day < firstDay {
			continue
		}
		countries[key.Country] += n
	}
	for country, n := range countries {
		report.Countries = append(report.Countries, CountryRow{Country: country, Requests: n})
	}
	sort.Slice(report.Countries, func(i, j int) bool { return report.Countries[i].Country < report.Countries[j].Country })
	return report, nil
}

// ReportFirstDay is the first UTC calendar day of the seven-day client and
// country windows.
func ReportFirstDay(now time.Time) string {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return today.AddDate(0, 0, -(ReportDays - 1)).Format("2006-01-02")
}

type storeError string

func (e storeError) Error() string { return string(e) }

const errStoreUnavailable = storeError("apidemand: store unavailable")
