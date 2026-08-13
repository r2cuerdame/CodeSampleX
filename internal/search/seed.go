package search

import (
	"context"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// SeedSampleDoc saves a sample's case + metadata row and indexes its FTS
// document (kind "sample") in one step. It is the shared ingestion helper
// for tests, shard warm-up, and sample install: anything that makes a
// sample locally searchable goes through here so the samples table and the
// FTS index never drift apart.
func SeedSampleDoc(ctx context.Context, db *localdb.DB, manifest domain.SampleManifest, sampleID, status string) error {
	c := manifest.Case
	if c.CaseID == "" {
		c.CaseID = c.ComputeID()
	}
	if err := db.SaveCase(ctx, c); err != nil {
		return err
	}
	manifest.Case = c
	row := localdb.SampleRow{
		SampleID:     sampleID,
		CaseID:       c.CaseID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
		Status:       status,
		License:      manifest.License,
		CreatedAt:    time.Now().UTC(),
	}
	if err := db.SaveSample(ctx, row); err != nil {
		return err
	}
	body := strings.Join(append([]string{c.Goal}, c.Contract...), "\n")
	return db.IndexDoc(ctx, sampleID, "sample",
		c.Goal, body,
		strings.Join(manifest.Packages, " "),
		strings.Join(manifest.Symbols, " "),
		"")
}
