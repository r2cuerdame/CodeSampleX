package config

import "testing"

// The setting was accepted, saved, echoed back by `csx config get`, and
// consulted by nothing at all. Excluded packages were still recorded as
// observations, still uploaded, and still named in the shard warm list —
// which asks the server about them BY NAME, so the exclusion leaked the
// very interest it was meant to hide.
func TestExcludedPackagesMatchHowPeopleWriteThem(t *testing.T) {
	const purl = "pkg:npm/@acme/widgets@1.2.3"
	for _, entry := range []string{
		"pkg:npm/@acme/widgets@1.2.3", // the exact purl
		"pkg:npm/@acme/widgets",       // without the version
		"npm/@acme/widgets",           // ecosystem and name
		"@acme/widgets",               // the name alone
		"  @acme/widgets  ",           // whitespace a person types
		"@ACME/Widgets",               // and case they do not think about
	} {
		c := &Config{ExcludedPackages: []string{entry}}
		if !c.IsExcluded(purl, "npm", "@acme/widgets") {
			t.Errorf("%q did not exclude %s", entry, purl)
		}
	}

	c := &Config{ExcludedPackages: []string{"@acme/widgets"}}
	if c.IsExcluded("pkg:npm/axios@1.19.0", "npm", "axios") {
		t.Error("an unrelated package was excluded")
	}
	if (&Config{}).IsExcluded(purl, "npm", "@acme/widgets") {
		t.Error("an empty list excluded something")
	}
	if (&Config{ExcludedPackages: []string{"", "   "}}).IsExcluded(purl, "npm", "@acme/widgets") {
		t.Error("a blank entry excluded everything")
	}
}

// A bare name excludes that name in every ecosystem. Someone writing
// "uuid" means uuid, not "uuid on npm but not on cargo", and a privacy
// setting that matches less than the user meant is worse than one that
// matches a little more.
func TestABareNameExcludesEveryEcosystem(t *testing.T) {
	c := &Config{ExcludedPackages: []string{"uuid"}}
	for _, tc := range []struct{ purl, eco, name string }{
		{"pkg:npm/uuid@14.0.1", "npm", "uuid"},
		{"pkg:cargo/uuid@1.11.0", "cargo", "uuid"},
		{"pkg:golang/github.com/google/uuid@v1.6.0", "golang", "uuid"},
	} {
		if !c.IsExcluded(tc.purl, tc.eco, tc.name) {
			t.Errorf("%s was not excluded", tc.purl)
		}
	}
}
