package samples

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// DefaultLicense is the default public-sample license (goal.md §7.5).
const DefaultLicense = "MIT-0"

// Created is the result of turning a clean-room directory into a local
// sample. Findings are advisory here: they block PUBLISH, never creation
// (the user still gets to preview and fix). MaxLevel records the honest
// verification ceiling — a contract-less sample can never claim more than
// L2_COMPILED (goal.md §7.5).
type Created struct {
	SampleID string
	Artifact []byte
	Findings []Finding
	MaxLevel domain.VerificationLevel
	Manifest domain.SampleManifest
}

// CreateFromDir validates the manifest, writes it as csx.json into dir,
// runs the leakage scan, and builds the canonical artifact. Nothing is
// transmitted; the caller decides about publishing after preview.
func CreateFromDir(ctx context.Context, dir string, manifest domain.SampleManifest) (*Created, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion == 0 {
		manifest.SchemaVersion = 1
	}
	if manifest.License == "" {
		manifest.License = DefaultLicense
	}
	if len(manifest.Packages) == 0 {
		return nil, errors.New("samples: manifest needs at least one package purl")
	}
	if manifest.Case.Goal == "" {
		return nil, errors.New("samples: manifest case needs a goal")
	}
	if manifest.Case.SchemaVersion == 0 {
		manifest.Case.SchemaVersion = 1
	}
	if manifest.Case.CaseID == "" {
		manifest.Case.CaseID = manifest.Case.ComputeID()
	}

	maxLevel := domain.L5MatrixPass
	if len(manifest.ContractCommand) == 0 {
		maxLevel = domain.L2Compiled // no contract ⇒ capped at L2 (goal.md §7.5)
	}

	// csx.json travels inside the artifact; canonical JSON keeps the
	// artifact — and therefore the sampleId — deterministic.
	raw := append(domain.MustCanonicalJSON(manifest), '\n')
	if err := os.WriteFile(filepath.Join(dir, "csx.json"), raw, 0o644); err != nil {
		return nil, fmt.Errorf("samples: write csx.json: %w", err)
	}

	findings, err := Scan(dir, ProvenanceOptions(dir))
	if err != nil {
		return nil, fmt.Errorf("samples: leakage scan: %w", err)
	}

	tgz, sampleID, err := BuildArtifact(dir)
	if err != nil {
		return nil, err
	}
	return &Created{
		SampleID: sampleID,
		Artifact: tgz,
		Findings: findings,
		MaxLevel: maxLevel,
		Manifest: manifest,
	}, nil
}

// NewCleanRoom creates an empty, collision-free clean-room workspace under
// home/samples/work/ for LLM generation (goal.md §9.3).
func NewCleanRoom(home string) (workdir string, err error) {
	base := filepath.Join(home, "samples", "work")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", fmt.Errorf("samples: clean room: %w", err)
	}
	workdir, err = os.MkdirTemp(base, "sample-*")
	if err != nil {
		return "", fmt.Errorf("samples: clean room: %w", err)
	}
	return workdir, nil
}
