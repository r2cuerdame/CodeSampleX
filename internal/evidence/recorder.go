package evidence

import (
	"context"
	"path/filepath"
	"sort"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/identity"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/scanner"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Recorder turns one wrapped command run into local aggregate
// observations (contract C14 steps 4–5). Only PUBLIC packages ever reach
// the observations table; PRIVATE and UNKNOWN packages are recorded in
// the local packages inventory only, which is never uploaded.
type Recorder struct {
	DB    *localdb.DB
	Ident *identity.Identity
	Cfg   *config.Config
}

// RecordRun records the outcome of one `csx run` execution.
//
//   - Every scanned package is upserted into the local packages table
//     (local diagnostics; never uploaded).
//   - Known commands record, per PUBLIC package, one package-level
//     observation (symbol "") and one observation per public symbol usage,
//     at the classified stage with PASS/FAIL from exitCode. On FAIL the
//     stderr tail is sanitized (stage-aware) and only the resulting
//     fingerprint + error code are attached — never the raw text.
//   - Unknown commands (profile.Known == false) prove nothing beyond
//     "these public packages are in use": they record only USED/PASS
//     package rows.
//   - Symbol sightings are stored with the rotating project bucket
//     derived from the absolute project path (HMAC, irreversible).
func (r *Recorder) RecordRun(ctx context.Context, dir string, res *scanner.ScanResult, profile scanner.CommandProfile, exitCode int, stderrTail string) error {
	if res == nil {
		return nil
	}
	now := time.Now().UTC()
	epoch := now.Format("2006-01-02")
	epochMonth := now.Format("2006-01")

	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	bucket := r.Ident.ProjectBucket(absDir, epochMonth)

	if err := r.DB.SaveEnvironment(ctx, res.Env); err != nil {
		return err
	}
	envHash := res.Env.Hash()

	// Upsert the full inventory locally; collect the PUBLIC subset.
	public := map[string]domain.PURL{}
	var publicNames []string
	for _, p := range res.Packages {
		if err := r.DB.UpsertPackage(ctx, p.PURL, p.Publicness); err != nil {
			return err
		}
		if p.Publicness == scanner.PublicnessPublic {
			key := p.PURL.String()
			if _, dup := public[key]; !dup {
				public[key] = p.PURL
				publicNames = append(publicNames, p.PURL.Name)
			}
		}
	}
	publicKeys := make([]string, 0, len(public))
	for k := range public {
		publicKeys = append(publicKeys, k)
	}
	sort.Strings(publicKeys)

	// Package-level project-bucket sighting (symbol ""), public only.
	// The batcher reads these to fill ObservationBatch.ProjectBucket.
	for _, key := range publicKeys {
		if err := r.DB.RecordSymbolUsage(ctx, public[key], "", domain.SymbolUnknown, bucket); err != nil {
			return err
		}
	}

	if !profile.Known {
		// Unclassified command: usage evidence only (C14).
		for _, key := range publicKeys {
			err := r.DB.RecordObservation(ctx, localdb.ObsKey{
				Epoch:   epoch,
				PURL:    key,
				EnvHash: envHash,
				Stage:   domain.StageUsed,
				Result:  domain.ResultPass,
			}, 1)
			if err != nil {
				return err
			}
		}
		return nil
	}

	result := domain.ResultPass
	errFP, errCode := "", ""
	if exitCode != 0 {
		result = domain.ResultFail
		san := sanitizer.Sanitize(stderrTail, profile.Stage, publicNames)
		errFP, errCode = san.Fingerprint, san.Code
	}

	for _, key := range publicKeys {
		err := r.DB.RecordObservation(ctx, localdb.ObsKey{
			Epoch:     epoch,
			PURL:      key,
			EnvHash:   envHash,
			Stage:     profile.Stage,
			Result:    result,
			ErrorFP:   errFP,
			ErrorCode: errCode,
		}, 1)
		if err != nil {
			return err
		}
	}

	for _, u := range res.Symbols {
		key := u.Package.String()
		if _, isPublic := public[key]; !isPublic || u.Family == "" {
			continue // PRIVATE/UNKNOWN symbols never enter any evidence table
		}
		if err := r.DB.RecordSymbolUsage(ctx, u.Package, u.Family, u.Confidence, bucket); err != nil {
			return err
		}
		err := r.DB.RecordObservation(ctx, localdb.ObsKey{
			Epoch:            epoch,
			PURL:             key,
			Symbol:           u.Family,
			SymbolConfidence: u.Confidence,
			EnvHash:          envHash,
			Stage:            profile.Stage,
			Result:           result,
			ErrorFP:          errFP,
			ErrorCode:        errCode,
		}, 1)
		if err != nil {
			return err
		}
	}
	return nil
}
