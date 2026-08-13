package registry

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// ServerCache adapts the serverstore packages table to the Cache interface,
// so the server-side ingest publicness gate (strict mode) reuses the exact
// same Checker the local client uses. Verdicts persist on packages.publicness
// with checked_at as the cache timestamp.
type ServerCache struct {
	Store serverstore.Store

	// Now is a test seam; nil means time.Now.
	Now func() time.Time
}

var _ Cache = (*ServerCache)(nil)

// GetPublicness returns the stored verdict for purl. A package row without a
// checked_at (evidence arrived but publicness was never verified) or with an
// UNKNOWN verdict is a cache miss, so the Checker re-queries the registry.
func (s *ServerCache) GetPublicness(ctx context.Context, purl string) (string, time.Time, bool) {
	pkg, ok, err := s.Store.GetPackage(ctx, purl)
	if err != nil || !ok || pkg.CheckedAt.IsZero() || pkg.Publicness == scanner.PublicnessUnknown {
		return "", time.Time{}, false
	}
	return pkg.Publicness, pkg.CheckedAt, true
}

// SetPublicness upserts the verdict with the current time as checked_at.
func (s *ServerCache) SetPublicness(ctx context.Context, purl, status string) error {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now()
	}
	return s.Store.UpsertPackage(ctx, serverstore.PackageRow{
		PURL:       p.String(),
		Ecosystem:  p.Ecosystem,
		Name:       p.Name,
		Version:    p.Version,
		Major:      p.Major(),
		Publicness: status,
		CheckedAt:  now,
	})
}
