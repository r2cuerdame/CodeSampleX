package fixclaims

import (
	"encoding/json"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// ReproducerSource is where the reproducer for a candidate came from, in
// the order the pipeline prefers them.
type ReproducerSource string

const (
	// ReproducerExistingSample: a published CSX sample already exercises
	// the symbol on this package. Cheapest, and already verified once.
	ReproducerExistingSample ReproducerSource = "EXISTING_SAMPLE"
	// ReproducerUpstream: the issue or PR carries a reproducer the worker
	// adapts minimally.
	ReproducerUpstream ReproducerSource = "UPSTREAM_REPRO"
	// ReproducerGenerated: the worker writes a minimal reproducer, which
	// must build and run before it is stored.
	ReproducerGenerated ReproducerSource = "GENERATED"
)

// ValidReproducerSource reports whether s is one of the three sources.
func ValidReproducerSource(s ReproducerSource) bool {
	return s == ReproducerExistingSample || s == ReproducerUpstream || s == ReproducerGenerated
}

// SampleCandidate is the bounded view of a published sample the resolver
// needs: its id and its manifest.
type SampleCandidate struct {
	SampleID     string
	ManifestJSON string
}

// Resolution is the resolver's answer: which source the worker should
// start from, and the samples that qualify when one already exists.
type Resolution struct {
	Source ReproducerSource `json:"source"`
	// ExistingSamples lists published samples that name the package and
	// one of the claim's symbols, best first. The worker re-runs the first
	// it can build at both versions rather than writing a new one.
	ExistingSamples []string `json:"existingSamples,omitempty"`
	// Instructions is the one-paragraph brief a worker follows for this
	// source.
	Instructions string `json:"instructions"`
}

// Resolve picks the reproducer source for a candidate from the samples the
// network already holds for the package. The order of preference is the
// issue's: an existing sample, then an upstream reproducer, then a
// generated one.
//
// A sample qualifies when its manifest names this package (any version --
// the reproducer is re-pinned per probe) and, if the claim names symbols,
// at least one of them. A claim with no symbols reuses only a sample whose
// case kind is FIX, since a HOW sample for the package is unlikely to
// exercise the failing behaviour.
func Resolve(c Candidate, samples []SampleCandidate) Resolution {
	c = c.Normalized()
	var matches []string
	for _, s := range samples {
		var m domain.SampleManifest
		if err := json.Unmarshal([]byte(s.ManifestJSON), &m); err != nil {
			continue
		}
		if !manifestNamesPackage(m, c) {
			continue
		}
		if len(c.Symbols) == 0 {
			if strings.EqualFold(m.Case.Kind, "FIX") {
				matches = append(matches, s.SampleID)
			}
			continue
		}
		if manifestNamesSymbol(m, c.Symbols) {
			matches = append(matches, s.SampleID)
		}
	}
	switch {
	case len(matches) > 0:
		return Resolution{
			Source:          ReproducerExistingSample,
			ExistingSamples: matches,
			Instructions: "A published sample already exercises this behaviour. Re-pin its manifest to each probe version " +
				"without changing its code or contract, run it, and submit each receipt. Fall back to writing a reproducer only " +
				"if the sample cannot build at the bad version.",
		}
	case c.UpstreamReproducer:
		return Resolution{
			Source: ReproducerUpstream,
			Instructions: "The upstream source carries a reproducer. Adapt it minimally into a csx.json case of kind FIX whose " +
				"contract asserts the behaviour the claim describes; keep the same code and assertion for every probe version.",
		}
	default:
		return Resolution{
			Source: ReproducerGenerated,
			Instructions: "Write the smallest case of kind FIX whose contract asserts the behaviour the claim describes. It must " +
				"build and run before it is stored, and the same code and assertion must be used at every probe version; " +
				"a compile that succeeds is not proof the bug is gone.",
		}
	}
}

func manifestNamesPackage(m domain.SampleManifest, c Candidate) bool {
	names := append([]string{m.Subject}, m.Packages...)
	names = append(names, m.Case.Packages...)
	for _, raw := range names {
		if raw == "" {
			continue
		}
		p, err := domain.ParsePURL(raw)
		if err != nil {
			continue
		}
		if p.Ecosystem == c.Ecosystem && strings.EqualFold(p.Name, c.Name) {
			return true
		}
	}
	return false
}

func manifestNamesSymbol(m domain.SampleManifest, symbols []string) bool {
	have := append([]string(nil), m.Symbols...)
	have = append(have, m.Case.Symbols...)
	for _, want := range symbols {
		for _, h := range have {
			if strings.EqualFold(h, want) || strings.HasSuffix(strings.ToLower(h), "."+strings.ToLower(want)) {
				return true
			}
		}
	}
	return false
}
