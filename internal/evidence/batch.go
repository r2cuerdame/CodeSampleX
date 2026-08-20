package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

const (
	// drainLimit bounds how many aggregate rows one drain reads at a time.
	drainLimit = 1000
	// uploadChunk must not exceed the server's documented per-request cap
	// (POST /v1/evidence/batches rejects more than 500 with 400).
	uploadChunk = 500
	// maxUploadPasses bounds one Upload call so a queue that keeps growing
	// cannot spin forever; the next sync picks up whatever is left.
	maxUploadPasses = 40
)

// Batcher drains pending local observation aggregates into anonymous
// wire batches (contract C14 step 6). Batches carry only the fields of
// domain.ObservationBatch — rotating IDs, hashes, and counts; never
// paths, project names, or raw logs.
type Batcher struct {
	DB    *localdb.DB
	Ident *identity.Identity
	Cfg   *config.Config
}

// Drain builds wire batches from every pending observation aggregate and
// marks the drained rows uploaded in the same step (read and mark
// back-to-back, no I/O in between — the localdb contract: rows carry
// FULL epoch counts and any later increment flips a row back to pending,
// so a full-count re-send delta-merges safely server-side). Ownership of
// the returned batches passes to the caller.
func (b *Batcher) Drain(ctx context.Context) ([]domain.ObservationBatch, error) {
	batches, keys, err := b.build(ctx)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	if err := b.DB.MarkObservationsUploaded(ctx, keys); err != nil {
		return nil, err
	}
	return batches, nil
}

// Upload drains pending observations and POSTs them to
// {serverURL}/v1/evidence/batches as {"batches":[...]}. Community mode
// only: any other mode is a silent no-op. On a non-2xx response or a
// transport error the affected rows are restored to pending so no
// evidence is lost while the server is unreachable (goal.md §25.F).
// It returns how many batches the server accepted.
//
// The upload is chunked to the server's documented per-request limit and
// repeats until the queue is empty. Posting a whole drain in one request
// meant any backlog larger than that limit was rejected wholesale and
// retried forever — the exact shape of a first sync after scanning a
// machine's worth of projects.
func (b *Batcher) Upload(ctx context.Context, httpClient *http.Client, serverURL string) (int, error) {
	if b.Cfg == nil || b.Cfg.Mode != config.ModeCommunity {
		return 0, nil
	}
	sent := 0
	// Rejections inside a 202 are collected and reported at the end. They
	// are not retried: the server refused this exact payload and would
	// refuse it again.
	var refused []rejectedBatch
	for pass := 0; pass < maxUploadPasses; pass++ {
		batches, keys, err := b.build(ctx)
		if err != nil {
			return sent, err
		}
		if len(batches) == 0 {
			// Nothing left to send — but a refusal from an earlier pass
			// still has to be reported, or it exits here as a clean run.
			return sent, rejectionError(refused)
		}
		for start := 0; start < len(batches); start += uploadChunk {
			end := min(start+uploadChunk, len(batches))
			chunkBatches, chunkKeys := batches[start:end], keys[start:end]

			// Mark before the network round-trip (atomically with the read):
			// marking after the POST would let a concurrent increment that
			// landed mid-flight be clobbered by the late mark.
			if err := b.DB.MarkObservationsUploaded(ctx, chunkKeys); err != nil {
				return sent, err
			}
			accepted, rejected, err := b.post(ctx, httpClient, serverURL, chunkBatches)
			if err != nil {
				// Failed delivery: flip the rows back to pending. A
				// zero-count re-record touches nothing but the flag.
				for _, k := range chunkKeys {
					_ = b.DB.RecordObservation(ctx, k, 0)
				}
				return sent, err
			}
			// What the server ACCEPTED, not what was handed to it. A 202
			// carries {accepted, rejected:[{index, reason}]}, and treating
			// the whole 2xx as success reported evidence as uploaded that
			// the server had explicitly refused -- already marked uploaded
			// locally, so it was gone, silently, with the count saying it
			// had landed.
			sent += accepted
			refused = append(refused, rejected...)
			// A 202 can accept part of a chunk and refuse the rest. The rows
			// were marked uploaded before the request to avoid clobbering a
			// concurrent increment, so explicitly restore only the refused
			// indexes. They remain durable for a later upload, while accepted
			// rows stay complete and contribute to sent.
			seen := make(map[int]bool, len(rejected))
			for _, rejection := range rejected {
				if rejection.Index < 0 || rejection.Index >= len(chunkKeys) || seen[rejection.Index] {
					continue
				}
				seen[rejection.Index] = true
				if err := b.DB.RecordObservation(ctx, chunkKeys[rejection.Index], 0); err != nil {
					return sent, fmt.Errorf("evidence: restore refused batch %d: %w", rejection.Index, err)
				}
			}
		}
		// Do not immediately retry a payload the server refused. Finish every
		// chunk from this drain, then leave the refused rows pending for the
		// next scheduled pass and report the refusal to the caller.
		if len(refused) > 0 {
			return sent, rejectionError(refused)
		}
	}
	return sent, rejectionError(refused)
}

// rejectedBatch is one refused batch of an ingest reply, mirroring the C5
// wire shape {index, reason}. It is declared here rather than imported from
// the server package: this is a client, and the only thing it shares with
// the server is the protocol.
type rejectedBatch struct {
	Index  int    `json:"index"`
	Reason string `json:"reason"`
}

// rejectionError turns refused batches into one non-fatal error, so the
// reason reaches the user instead of the evidence disappearing quietly.
func rejectionError(refused []rejectedBatch) error {
	if len(refused) == 0 {
		return nil
	}
	reason := refused[0].Reason
	if reason == "" {
		reason = "no reason given"
	}
	if len(refused) == 1 {
		return fmt.Errorf("evidence: the server refused 1 batch: %s", reason)
	}
	return fmt.Errorf("evidence: the server refused %d batches, first: %s", len(refused), reason)
}

// build assembles batches from pending rows without marking anything.
func (b *Batcher) build(ctx context.Context) ([]domain.ObservationBatch, []localdb.ObsKey, error) {
	rows, err := b.DB.PendingObservations(ctx, drainLimit)
	if err != nil {
		return nil, nil, err
	}
	envs := map[string]domain.EnvironmentFingerprint{}
	buckets := map[string][]localdb.SymbolUsageRow{}
	var batches []domain.ObservationBatch
	var keys []localdb.ObsKey
	for _, row := range rows {
		env, cached := envs[row.EnvHash]
		if !cached {
			fp, found, err := b.DB.GetEnvironment(ctx, row.EnvHash)
			if err != nil {
				return nil, nil, err
			}
			if !found {
				continue // no fingerprint on file: leave the row pending
			}
			envs[row.EnvHash] = fp
			env = fp
		}
		batch := domain.ObservationBatch{
			SchemaVersion:    1,
			Epoch:            row.Epoch,
			AnonID:           b.Ident.AnonID(row.Epoch),
			ProjectBucket:    b.bucketFor(ctx, buckets, row),
			Package:          row.PURL,
			Symbol:           row.Symbol,
			Environment:      env,
			Stage:            row.Stage,
			Result:           row.Result,
			ObservationCount: row.Count,
			ErrorFingerprint: row.ErrorFP,
			ErrorCode:        row.ErrorCode,
			Direct:           row.Direct,
			Coresident:       row.Coresident,
		}
		if row.Symbol != "" {
			batch.SymbolConfidence = row.SymbolConfidence
		}
		batches = append(batches, batch)
		keys = append(keys, row.ObsKey)
	}
	return batches, keys, nil
}

// bucketFor resolves the rotating project bucket recorded for this
// aggregate: the symbol-usage sighting matching the row's symbol (the
// recorder stores a symbol=="" sighting per public package), falling
// back to the package's most recent sighting, else "". Buckets are
// HMAC-derived and rotate monthly; they are dedup hints, never identity.
func (b *Batcher) bucketFor(ctx context.Context, memo map[string][]localdb.SymbolUsageRow, row localdb.ObsRow) string {
	usages, cached := memo[row.PURL]
	if !cached {
		p, err := domain.ParsePURL(row.PURL)
		if err != nil {
			memo[row.PURL] = nil
			return ""
		}
		usages, err = b.DB.SymbolUsages(ctx, p)
		if err != nil {
			usages = nil
		}
		memo[row.PURL] = usages
	}
	var exact, latest string
	var exactAt, latestAt time.Time
	for _, u := range usages {
		if u.Symbol == row.Symbol && (exact == "" || u.LastSeen.After(exactAt)) {
			exact, exactAt = u.ProjectBucket, u.LastSeen
		}
		if latest == "" || u.LastSeen.After(latestAt) {
			latest, latestAt = u.ProjectBucket, u.LastSeen
		}
	}
	if exact != "" {
		return exact
	}
	return latest
}

// post sends one batch payload; any non-2xx status is an error.
// post delivers one chunk and reports how many batches the server actually
// took, plus the ones it refused.
func (b *Batcher) post(ctx context.Context, client *http.Client, serverURL string,
	batches []domain.ObservationBatch) (int, []rejectedBatch, error) {

	body, err := json.Marshal(map[string]any{"batches": batches})
	if err != nil {
		return 0, nil, err
	}
	url := strings.TrimSuffix(serverURL, "/") + "/v1/evidence/batches"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	// Keep the server's reason: a bare status turned a one-line contract
	// mismatch ("too many batches in one request") into a silent, endless
	// retry that looked like a network problem.
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if detail := strings.TrimSpace(string(reply)); detail != "" {
			return 0, nil, fmt.Errorf("evidence: upload: server returned %s: %s", resp.Status, detail)
		}
		return 0, nil, fmt.Errorf("evidence: upload: server returned %s", resp.Status)
	}
	// Accepted is a POINTER so "the field was absent" is distinguishable
	// from "the server accepted zero". A 2xx with no ack body at all means
	// the request was taken; reading that as zero-accepted would report
	// every upload as having delivered nothing.
	var ack struct {
		Accepted *int            `json:"accepted"`
		Rejected []rejectedBatch `json:"rejected"`
	}
	if json.Unmarshal(reply, &ack) != nil || ack.Accepted == nil {
		return len(batches) - len(ack.Rejected), ack.Rejected, nil
	}
	return *ack.Accepted, ack.Rejected, nil
}
