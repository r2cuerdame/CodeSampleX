package config

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// What a REAL caller passes: recorder/daemon call IsExcluded with
// PURL.String(), which percent-encodes the npm scope.
func TestZZScratchExcludeRealCallerForm(t *testing.T) {
	p, err := domain.ParsePURL("pkg:npm/@acme/widgets@1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	callerPurl := p.String()
	t.Logf("caller purl = %q name=%q", callerPurl, p.Name)

	for _, entry := range []string{
		"pkg:npm/@acme/widgets@1.2.3",
		"pkg:npm/@acme/widgets",
		"npm/@acme/widgets",
		"@acme/widgets",
		"pkg:npm/%40acme/widgets",
	} {
		c := &Config{ExcludedPackages: []string{entry}}
		t.Logf("entry %-32q excluded=%v", entry, c.IsExcluded(callerPurl, p.Ecosystem, p.Name))
	}

	// the value the project's own config test uses as an example
	pl, _ := domain.ParsePURL("pkg:npm/leftpad@1.3.0")
	c := &Config{ExcludedPackages: []string{"pkg:npm/leftpad@*"}}
	t.Logf("wildcard entry excluded=%v", c.IsExcluded(pl.String(), pl.Ecosystem, pl.Name))
}
