package config

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The exclusion doc promises that pkg:npm/@acme/widgets excludes
// pkg:npm/@acme/widgets@1.2.3. The caller passes PURL.String(), which
// percent-encodes the scope, so the two strings never matched.
func TestPurlStringFormOfAScopedPackage(t *testing.T) {
	p := domain.PURL{Ecosystem: "npm", Name: "@acme/widgets", Version: "1.2.3"}
	t.Logf("PURL.String() = %q", p.String())
	c := &Config{ExcludedPackages: []string{"pkg:npm/@acme/widgets"}}
	if !c.IsExcluded(p.String(), p.Ecosystem, p.Name) {
		t.Errorf("the purl-without-version form did not exclude %s", p.String())
	}
}
