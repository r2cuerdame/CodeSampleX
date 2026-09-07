package admin

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

type coverageMemoStore struct {
	*serverstore.Fake
	value     []serverstore.FarmAxisCoverage
	err       error
	calls     int
	afterRead func()
}

func (s *coverageMemoStore) FarmCoverage(ctx context.Context) ([]serverstore.FarmAxisCoverage, error) {
	s.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.afterRead != nil {
		s.afterRead()
	}
	return append([]serverstore.FarmAxisCoverage(nil), s.value...), s.err
}

func TestFarmCoverageMemoKeepsLastGoodThroughTTLAndFailureDeferral(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	initial := []serverstore.FarmAxisCoverage{{OS: "linux", Ecosystem: "golang", Observed: 10, Proven: 4}}
	fresh := []serverstore.FarmAxisCoverage{{OS: "linux", Ecosystem: "golang", Observed: 12, Proven: 8}}
	store := &coverageMemoStore{Fake: serverstore.NewFake(), value: initial}
	h := &handler{farmStats: store, now: func() time.Time { return now }}
	read := func(want []serverstore.FarmAxisCoverage, wantAt time.Time, wantCalls int) {
		t.Helper()
		got, at := h.coverage(t.Context(), now)
		if !reflect.DeepEqual(got, want) || !at.Equal(wantAt) || store.calls != wantCalls {
			t.Fatalf("coverage=%+v at=%s calls=%d, want %+v at=%s calls=%d", got, at, store.calls, want, wantAt, wantCalls)
		}
	}
	firstAt := now
	read(initial, firstAt, 1)
	store.value = fresh
	now = now.Add(farmCoverageTTL - time.Second)
	read(initial, firstAt, 1)
	now = now.Add(2 * time.Second)
	store.err = errors.New("database query failed")
	read(initial, firstAt, 2)
	deferredUntil := now.Add(farmCoverageBackoff)
	if !h.farmCoverage.retryAt.Equal(deferredUntil) {
		t.Fatalf("failure defer=%s, want %s", h.farmCoverage.retryAt, deferredUntil)
	}
	store.err = nil
	now = deferredUntil.Add(-time.Second)
	read(initial, firstAt, 2)
	now = deferredUntil
	read(fresh, now, 3)
	if !h.farmCoverage.retryAt.IsZero() {
		t.Fatal("successful recovery retained shared backoff")
	}
	// A successful empty result is computed data, not a never-loaded cache.
	store.value = nil
	now = now.Add(farmCoverageTTL)
	emptyAt := now
	read(nil, emptyAt, 4)
	now = now.Add(time.Minute)
	read(nil, emptyAt, 4)
}

func TestFarmCoverageCanceledCallerDoesNotInstallSharedBackoff(t *testing.T) {
	for _, warm := range []bool{false, true} {
		for _, expired := range []bool{false, true} {
			name := "cold/cancel"
			if warm {
				name = "stale/cancel"
			}
			if expired {
				name += "-deadline"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
				fresh := []serverstore.FarmAxisCoverage{{OS: "windows", Ecosystem: "golang", Observed: 3, Proven: 2}}
				store := &coverageMemoStore{Fake: serverstore.NewFake(), value: fresh}
				h := &handler{farmStats: store, now: func() time.Time { return now }}
				if warm {
					h.farmCoverage.value = []serverstore.FarmAxisCoverage{{OS: "linux", Observed: 1}}
					h.farmCoverage.at = now.Add(-2 * farmCoverageTTL)
				}
				old, oldAt := h.farmCoverage.value, h.farmCoverage.at
				ctx, cancel := context.WithCancel(t.Context())
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				} else {
					cancel()
				}
				got, at := h.coverage(ctx, now)
				cancel()
				if !reflect.DeepEqual(got, old) || !at.Equal(oldAt) {
					t.Fatal("canceled caller erased the last-good coverage")
				}
				if !h.farmCoverage.retryAt.IsZero() {
					t.Fatal("canceled caller installed a shared cooldown")
				}
				got, at = h.coverage(t.Context(), now)
				if !reflect.DeepEqual(got, fresh) || !at.Equal(now) || store.calls != 2 {
					t.Fatalf("healthy caller could not refresh immediately: value=%+v at=%s calls=%d", got, at, store.calls)
				}
			})
		}
	}
}

func TestFarmCoverageAgeAndDeferralStartWhenReadCompletes(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	store := &coverageMemoStore{Fake: serverstore.NewFake(), afterRead: func() { now = now.Add(5 * time.Second) }}
	h := &handler{farmStats: store, now: func() time.Time { return now }}
	_, at := h.coverage(t.Context(), now)
	if !at.Equal(now) {
		t.Fatalf("coverage age predates completion: %s, want %s", at, now)
	}
	now = now.Add(farmCoverageTTL)
	store.err = errors.New("database query failed")
	h.coverage(t.Context(), now)
	if !h.farmCoverage.retryAt.Equal(now.Add(farmCoverageBackoff)) {
		t.Fatal("failure deferral started before read completion")
	}
}

func TestFarmCoveragePanelDisplaysMeasurementAge(t *testing.T) {
	js, err := adminStaticFS.ReadFile("static/admin.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(js), "커버리지 집계 ${since(data.coverageAt)}") || !strings.Contains(string(js), "coverage.appendChild(age)") {
		t.Fatal("coverage payload timestamp is not rendered beside retained values")
	}
	if adminTimeOrEmpty(time.Time{}) != "" {
		t.Fatal("never-computed coverage advertises a measurement timestamp")
	}
}
