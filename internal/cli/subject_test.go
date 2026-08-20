package cli

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
)

// A receipt resolves the whole lockfile, so the snapshot targets had to guess
// which package a symbol belonged to by counting how many each sample pulled
// in. The propose command does not have to guess: the authoring queue assigns
// one exact package and that is what --package carries.
func TestProposedManifestStatesItsSubjectWhenOnePackageWasNamed(t *testing.T) {
	m := proposalManifest(samples.SanitizedSpec{
		Goal: "g", Packages: []string{"pkg:gem/faraday@2.9.0"},
	}, domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "gem"})
	if m.Subject != "pkg:gem/faraday@2.9.0" {
		t.Errorf("subject = %q, want the one package the sample was written for", m.Subject)
	}
}

// Several packages and no way to tell which is the subject. Saying nothing
// leaves the inference to run, which is what it is for; naming the first
// would be a guess wearing a fact's clothes.
func TestProposedManifestStaysSilentWhenTheSubjectIsAmbiguous(t *testing.T) {
	m := proposalManifest(samples.SanitizedSpec{
		Goal: "g", Packages: []string{"pkg:gem/faraday@2.9.0", "pkg:gem/minitest@5.27.0"},
	}, domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "gem"})
	if m.Subject != "" {
		t.Errorf("subject = %q, want none: two packages were named", m.Subject)
	}
}

// The field is omitempty, so every sample authored before it existed still
// canonicalises — and therefore still hashes — exactly as it did.
func TestSubjectIsAbsentFromCanonicalJSONWhenUnset(t *testing.T) {
	m := domain.SampleManifest{SchemaVersion: 1, Packages: []string{"pkg:npm/a@1.0.0"}}
	if got := string(domain.MustCanonicalJSON(m)); contains(got, `"subject"`) {
		t.Errorf("canonical JSON carries an empty subject: %s", got)
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
