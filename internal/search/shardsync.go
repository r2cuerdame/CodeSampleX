// Package search implements the local search side of CodeSampleX: shard
// syncing/warming plus (in sibling files) ranking and grading over the
// local SQLite store. Shards are read-only compatibility summaries pulled
// from the server; nothing in this package uploads anything.
package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Syncer performs ETag-aware conditional GETs of compatibility shards
// (contract C6) and indexes their contents into the local FTS table so
// offline search keeps working during server outages (goal.md §3.9).
type Syncer struct {
	DB        *localdb.DB
	HTTP      *http.Client
	ServerURL string
}

// shardDoc mirrors the C6 wire shape (schemas/v1/shard.json). Only the
// fields the local index needs are declared; unknown fields are ignored so
// additive server changes never break old clients.
type shardDoc struct {
	SchemaVersion int          `json:"schemaVersion"`
	Key           string       `json:"key"`
	Packages      []shardEntry `json:"packages"`
}

type shardEntry struct {
	PURL    string        `json:"purl"`
	Symbols []shardSymbol `json:"symbols"`
	Samples []shardSample `json:"samples"`
}

type shardSymbol struct {
	Family   string        `json:"family"`
	Stats    shardStats    `json:"stats"`
	Failures []shardChange `json:"failures"`
}

type shardStats struct {
	ObservationCount  int64                     `json:"observationCount"`
	UniquePeerBuckets int64                     `json:"uniquePeerBuckets"`
	PassRate          float64                   `json:"passRate"`
	ByStage           map[string]map[string]any `json:"byStage"`
	Confidence        string                    `json:"confidence"`
	LastSeen          string                    `json:"lastSeen"`
}

type shardChange struct {
	ErrorCode   string            `json:"errorCode"`
	Fingerprint string            `json:"fingerprint"`
	Count       int64             `json:"count"`
	EnvSummary  map[string]string `json:"envSummary"`
}

type shardSample struct {
	SampleID string `json:"sampleId"`
	Goal     string `json:"goal"`
	Status   string `json:"status"`
	License  string `json:"license"`
}

// SyncKey conditionally fetches one shard ("npm/axios/1") from
// GET {server}/v1/shards/{key}.
//
//	304 → the cached copy is current; only synced_at is re-stamped.
//	200 → body + ETag stored, contents (re)indexed into FTS.
//	404 → the shard does not exist yet server-side; nothing is removed and
//	      no error is returned.
//	network / other status → error, so the caller can queue a retry.
func (s *Syncer) SyncKey(ctx context.Context, key string) error {
	prev, havePrev, err := s.DB.GetShard(ctx, key)
	if err != nil {
		return err
	}

	url := strings.TrimSuffix(s.ServerURL, "/") + "/v1/shards/" + key
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if havePrev && prev.ETag != "" {
		req.Header.Set("If-None-Match", prev.ETag)
	}

	client := s.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("shard sync %s: %w", key, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		// Re-stamp synced_at only, keeping the cached body and ETag.
		return s.DB.SaveShard(ctx, key, prev.ETag, prev.JSON)
	case http.StatusNotFound:
		// Shard may simply not be generated yet; keep whatever we have.
		return nil
	case http.StatusOK:
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("shard sync %s: read body: %w", key, err)
		}
		if err := s.DB.SaveShard(ctx, key, resp.Header.Get("ETag"), string(body)); err != nil {
			return err
		}
		return s.indexShard(ctx, key, body)
	default:
		return fmt.Errorf("shard sync %s: unexpected status %d", key, resp.StatusCode)
	}
}

// indexShard writes one FTS doc per symbol entry and per sample entry.
// Bodies hold only aggregate keywords (confidence, stages, error codes) and
// case goals — never anything project-identifying.
func (s *Syncer) indexShard(ctx context.Context, key string, body []byte) error {
	var doc shardDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("shard sync %s: parse: %w", key, err)
	}
	for _, pkg := range doc.Packages {
		for _, sym := range pkg.Symbols {
			var codes []string
			for _, f := range sym.Failures {
				if f.ErrorCode != "" {
					codes = append(codes, f.ErrorCode)
				}
			}
			var stages []string
			for stage := range sym.Stats.ByStage {
				stages = append(stages, stage)
			}
			bodyParts := append([]string{sym.Stats.Confidence}, stages...)
			bodyParts = append(bodyParts, codes...)
			if err := s.DB.IndexDoc(ctx,
				"shard:"+key+":"+sym.Family,
				"shard-symbol",
				sym.Family,
				strings.TrimSpace(strings.Join(bodyParts, " ")),
				pkg.PURL,
				sym.Family,
				strings.Join(codes, " "),
			); err != nil {
				return err
			}
		}
		for _, smp := range pkg.Samples {
			if smp.SampleID == "" {
				continue
			}
			if err := s.DB.IndexDoc(ctx,
				"sample:"+smp.SampleID,
				"sample",
				smp.Goal,
				strings.TrimSpace(smp.Goal+" "+smp.Status),
				pkg.PURL,
				"",
				"",
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncAll syncs every key, continuing past individual failures so one
// unreachable shard (or a down server) never blocks the rest (goal.md §3.9).
// All failures are aggregated into the returned error.
func (s *Syncer) SyncAll(ctx context.Context, keys []string) error {
	var errs []error
	for _, key := range keys {
		if err := s.SyncKey(ctx, key); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WarmKeys builds the shard warm list in §11.2 priority order:
// current project deps → recently used → global HOT → pinned,
// deduplicated with first occurrence winning. Keys are
// "ecosystem/name/major" (major from PURL.Major, e.g. "1", golang "v1").
func WarmKeys(projectPkgs []domain.PURL, recent, hot, pinned []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}
	for _, p := range projectPkgs {
		add(p.Ecosystem + "/" + p.Name + "/" + p.Major())
	}
	for _, k := range recent {
		add(k)
	}
	for _, k := range hot {
		add(k)
	}
	for _, k := range pinned {
		add(k)
	}
	return out
}
