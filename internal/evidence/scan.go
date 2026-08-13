// Package evidence implements the local evidence loop behind `csx run`
// (goal.md §8.3, contract C14): scan the project, run the user's command,
// record aggregate observations for PUBLIC packages only, and drain them
// into anonymous wire batches. Raw logs and paths never enter any
// uploadable structure; PRIVATE and UNKNOWN packages never enter the
// observations table at all.
package evidence

import (
	"context"
	"time"

	"github.com/r2cuerdame/codesamplex/adapters"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/registry"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Scan detects the ecosystems present in dir and runs the scan
// orchestrator (contract C14 step 1). A nil checker skips the publicness
// pass — packages stay UNKNOWN and are excluded from evidence. This
// wrapper lives here rather than in internal/scanner because both
// adapters and internal/registry import internal/scanner.
func Scan(ctx context.Context, dir string, checker *registry.Checker) (*scanner.ScanResult, error) {
	var pc scanner.PackageChecker
	if checker != nil {
		pc = checker
	}
	return scanner.Scan(ctx, dir, adapters.Detect(dir), pc)
}

// PublicnessCache adapts localdb's packages table to registry.Cache so
// publicness verdicts persist across runs (24h TTL enforced by the
// checker via the stored checked_at stamp).
type PublicnessCache struct {
	DB *localdb.DB
}

var _ registry.Cache = PublicnessCache{}

// GetPublicness looks up a cached verdict by canonical purl string.
func (c PublicnessCache) GetPublicness(ctx context.Context, purl string) (string, time.Time, bool) {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return "", time.Time{}, false
	}
	return c.DB.GetPublicness(ctx, p)
}

// SetPublicness stores a registry check outcome.
func (c PublicnessCache) SetPublicness(ctx context.Context, purl, status string) error {
	p, err := domain.ParsePURL(purl)
	if err != nil {
		return err
	}
	return c.DB.SetPublicness(ctx, p, status)
}
