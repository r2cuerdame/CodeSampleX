package lightsail

import (
	"regexp"
	"strings"
	"testing"
)

func TestDeployProductionRequiresTrackingIssueWithBackwardCompatibleAlias(t *testing.T) {
	wrapper := readDeployFixture(t, "deploy-production.ps1")

	for _, required := range []string{
		`[Alias("LinearIssue")]`,
		`[Parameter(Mandatory)][string]$TrackingIssue`,
		`trackingIssue = $TrackingIssue`,
		`invalid canonical GitHub tracking issue identifier`,
	} {
		if !strings.Contains(wrapper, required) {
			t.Errorf("deploy-production.ps1 is missing %q", required)
		}
	}
}

func TestTrackingIssueValidationPattern(t *testing.T) {
	wrapper := readDeployFixture(t, "deploy-production.ps1")
	m := regexp.MustCompile(`\$TrackingIssue -notmatch '([^']+)'`).FindStringSubmatch(wrapper)
	if m == nil {
		t.Fatal("could not extract TrackingIssue validation pattern from deploy-production.ps1")
	}
	pattern := m[1]
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("invalid tracking issue regex %q: %v", pattern, err)
	}

	valid := []string{
		"213",
		"#213",
		"#174",
		"https://github.com/r2cuerdame/CodeSampleX/issues/213",
		"https://github.com/org/repo/issues/1",
	}
	for _, tc := range valid {
		if !re.MatchString(tc) {
			t.Errorf("expected valid tracking issue %q to match pattern %q", tc, pattern)
		}
	}

	invalid := []string{
		"R2C-159", "CSX-213",
		"",
		"#",
		"abc",
		"invalid-issue",
		"#0",
		"0",
		"https://linear.app/issue/123",
		"!213",
	}
	for _, tc := range invalid {
		if re.MatchString(tc) {
			t.Errorf("expected invalid tracking issue %q to fail pattern %q", tc, pattern)
		}
	}
}
