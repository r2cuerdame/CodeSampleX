package web

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// homeAssetsShown is how many of each the front page carries. Few enough that
// the shape is unmistakable and the page stays one screen; the collections
// behind them hold the rest.
const homeAssetsShown = 3

// homeMovedShown is larger because each row is one line.
const homeMovedShown = 6

const (
	homeAssetsTTL     = 5 * time.Minute
	homeAssetsTimeout = 20 * time.Second
	homeAssetsRetry   = 30 * time.Second
)

// homeSample is one published sample as the front page shows it.
type homeSample struct {
	Goal    string
	Coord   string
	Context string
	Href    string
}

// homeMoved is one child that moved under a release.
type homeMoved struct {
	ParentName string
	ChildName  string
	CountText  string
	Href       string
}

// homeAssetCache holds the two product strips the front page leads with.
//
// Both are reads the page must never wait on: the landing page is the first
// thing a visitor sees and the first thing a deployment smoke checks, and the
// findings cache already exists because a whole-corpus scan inside a request
// once timed that smoke out without sending a byte.
type homeAssetCache struct {
	mu         sync.Mutex
	samples    []SampleListItem
	moved      []MovedDependency
	at         time.Time
	refreshing bool
	retryAt    time.Time
}

// homeAssets returns the last complete strips and starts one bounded refresh
// when they are cold or stale.
//
// Cold returns nothing, and the sections simply do not render. An empty strip
// is honest; a placeholder row would be this network inventing activity, which
// is the one thing the front page must never do.
func (s *site) homeAssets() ([]SampleListItem, []MovedDependency) {
	s.home.mu.Lock()
	now := time.Now()
	samples, moved := s.home.samples, s.home.moved
	if !s.home.at.After(now.Add(-homeAssetsTTL)) &&
		!s.home.refreshing && !now.Before(s.home.retryAt) {
		s.home.refreshing = true
		go s.refreshHomeAssets(homeAssetsTimeout)
	}
	s.home.mu.Unlock()
	return samples, moved
}

func (s *site) refreshHomeAssets(timeout time.Duration) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("web: panic refreshing home assets: %v", recovered)
			s.failHomeAssets()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	samples, _, err := s.d.Store.SamplesPage(ctx, 0, homeAssetsShown)
	if err != nil {
		s.failHomeAssets()
		return
	}
	moved, err := s.d.Store.MovedDependencies(ctx, homeMovedShown)
	if err != nil {
		s.failHomeAssets()
		return
	}
	s.home.mu.Lock()
	s.home.samples, s.home.moved, s.home.at = samples, moved, time.Now()
	s.home.refreshing = false
	s.home.retryAt = time.Time{}
	s.home.mu.Unlock()
}

// failHomeAssets keeps the last good strips and backs off.
func (s *site) failHomeAssets() {
	s.home.mu.Lock()
	s.home.refreshing = false
	s.home.retryAt = time.Now().Add(homeAssetsRetry)
	s.home.mu.Unlock()
}

// buildHomeSamples renders the sample strip.
//
// The goal sentence leads, never the sample id: a reader is looking for an
// answer they can reuse, and a content hash is not one.
func buildHomeSamples(rows []SampleListItem) []homeSample {
	out := make([]homeSample, 0, len(rows))
	for _, r := range rows {
		if strings.TrimSpace(r.Goal) == "" || r.SampleID == "" {
			continue
		}
		coord := r.Name
		if r.Version != "" {
			coord += "@" + r.Version
		}
		if len(r.Symbols) > 0 {
			coord += " · " + r.Symbols[0]
		}
		out = append(out, homeSample{
			Goal: r.Goal, Coord: strings.TrimSpace(coord), Context: r.Context,
			Href: "/samples/" + r.SampleID,
		})
	}
	return out
}

// buildHomeMoved renders the dependency strip.
func buildHomeMoved(lang string, rows []MovedDependency) []homeMoved {
	out := make([]homeMoved, 0, len(rows))
	for _, r := range rows {
		if r.ParentName == "" || r.ChildName == "" {
			continue
		}
		out = append(out, homeMoved{
			ParentName: r.ParentName, ChildName: r.ChildName,
			CountText: i18n.T(lang, "landing.moved_count",
				i18n.FormatInt(lang, int64(r.Versions)),
				i18n.FormatInt(lang, int64(r.Releases))),
			Href: pkgHref(r.Ecosystem, r.ParentName),
		})
	}
	return out
}
