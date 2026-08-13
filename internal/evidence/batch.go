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

// drainLimit bounds how many aggregate rows one drain/upload handles.
const drainLimit = 1000

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
// transport error the drained rows are restored to pending so no
// evidence is lost while the server is unreachable (goal.md §25.F).
// It returns how many batches the server accepted.
func (b *Batcher) Upload(ctx context.Context, httpClient *http.Client, serverURL string) (int, error) {
	if b.Cfg == nil || b.Cfg.Mode != config.ModeCommunity {
		return 0, nil
	}
	batches, keys, err := b.build(ctx)
	if err != nil {
		return 0, err
	}
	if len(batches) == 0 {
		return 0, nil
	}
	// Mark before the network round-trip (atomically with the read):
	// marking after the POST would let a concurrent increment that landed
	// mid-flight be clobbered by the late mark.
	if err := b.DB.MarkObservationsUploaded(ctx, keys); err != nil {
		return 0, err
	}
	if err := b.post(ctx, httpClient, serverURL, batches); err != nil {
		// Failed delivery: flip the rows back to pending. A zero-count
		// re-record touches nothing but the uploaded flag.
		for _, k := range keys {
			_ = b.DB.RecordObservation(ctx, k, 0)
		}
		return 0, err
	}
	return len(batches), nil
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
func (b *Batcher) post(ctx context.Context, client *http.Client, serverURL string, batches []domain.ObservationBatch) error {
	body, err := json.Marshal(map[string]any{"batches": batches})
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(serverURL, "/") + "/v1/evidence/batches"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // drain for keep-alive
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("evidence: upload: server returned %s", resp.Status)
	}
	return nil
}
