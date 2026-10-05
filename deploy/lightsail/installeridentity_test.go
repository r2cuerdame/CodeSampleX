package lightsail

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestDeploymentPinsAndVerifiesTheInstallerReleaseBeforePromotion(t *testing.T) {
	raw, err := os.ReadFile("deploy.ps1")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, required := range []string{
		`Invoke-DeployProcess git @("-C", $repo, "tag", "--points-at", $revision, "--list", "v*") 10`,
		`$releaseTags.Count -lt 1`,
		`Sort-Object { [version]($_ -replace '^v', '') }`,
		`"csx-bootstrap-stable.json"`,
		`./csx-linux-amd64 update verify-release . {2}`,
		`$stageFiles, $requiredReleaseAssets.Count, $tag`,
		`test ! -L /opt/codesamplex/dist`,
		`$stillMounted -cne $mountedReleaseBefore`,
		`$mountedReleaseAfter -cne $tag`,
	} {
		if !strings.Contains(s, required) {
			t.Errorf("missing installer deployment contract %q", required)
		}
	}
	if strings.Contains(s, `gh release view --repo r2cuerdame/CodeSampleX --json tagName`) {
		t.Fatal("deployment assets can drift from target revision to latest release")
	}
	verify := strings.Index(s, "Invoke-Remote $stageValidation")
	promote := strings.Index(s, "mv /opt/codesamplex/dist.stage /opt/codesamplex/dist")
	if verify < 0 || promote < verify {
		t.Fatal("installer assets can be promoted before signed verification")
	}
	compose, err := os.ReadFile("../docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), `${CSX_DIST_HOST_DIR:-../dist}:/data/dist:ro`) {
		t.Fatal("release generation is no longer pinned by a directory bind mount")
	}
}

func TestDeploymentResolvesHighestSemverWhenMultipleTagsPointAtRevision(t *testing.T) {
	pwsh := os.Getenv("CSX_TEST_PWSH")
	if pwsh == "" {
		pwsh, _ = exec.LookPath("pwsh")
	}
	if pwsh == "" {
		t.Skip("PowerShell 7 required")
	}

	raw, err := os.ReadFile("deploy.ps1")
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)

	// Extract the tag resolution logic from deploy.ps1
	pattern := regexp.MustCompile(`(?s)(\$releaseTags = @\(Invoke-DeployProcess git.*?\n\s*\$tag = \[string\]\$releaseTags\[[^\]]+\])`)
	m := pattern.FindStringSubmatch(script)
	if m == nil {
		t.Fatal("deploy.ps1 has no release tag resolution block to test")
	}
	tagResolutionBlock := m[1]

	runWithTags := func(tags []string) (string, error) {
		tagsLiteral := "@("
		for i, tag := range tags {
			if i > 0 {
				tagsLiteral += ", "
			}
			tagsLiteral += fmt.Sprintf("'%s'", tag)
		}
		tagsLiteral += ")"

		psScript := fmt.Sprintf(`$ErrorActionPreference = 'Stop'
function Invoke-DeployProcess { return %s }
$repo = '.'
$revision = '560dfe0f28dce124085e98fd0b3ab128b4a19a9e'
%s
Write-Output "RESOLVED_TAG:$tag"
`, tagsLiteral, tagResolutionBlock)

		cmd := exec.Command(pwsh, "-NoProfile", "-Command", psScript)
		out, err := cmd.CombinedOutput()
		outStr := strings.TrimSpace(string(out))
		if err != nil {
			return "", fmt.Errorf("script failed: %v: %s", err, outStr)
		}
		for _, line := range strings.Split(outStr, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "RESOLVED_TAG:") {
				return strings.TrimPrefix(line, "RESOLVED_TAG:"), nil
			}
		}
		return "", fmt.Errorf("no tag output found in: %s", outStr)
	}

	// Case 1: Multiple tags on the same revision (e.g. v0.2.2 and v0.2.3)
	tag, err := runWithTags([]string{"v0.2.2", "v0.2.3"})
	if err != nil {
		t.Fatalf("failed to resolve tag when multiple tags point at revision: %v", err)
	}
	if tag != "v0.2.3" {
		t.Fatalf("expected highest semver v0.2.3, got %s", tag)
	}

	// Case 2: Order independence
	tag, err = runWithTags([]string{"v0.2.3", "v0.2.2"})
	if err != nil {
		t.Fatalf("failed to resolve tag when order is inverted: %v", err)
	}
	if tag != "v0.2.3" {
		t.Fatalf("expected highest semver v0.2.3, got %s", tag)
	}

	// Case 3: Single tag
	tag, err = runWithTags([]string{"v0.2.1"})
	if err != nil {
		t.Fatalf("failed to resolve single tag: %v", err)
	}
	if tag != "v0.2.1" {
		t.Fatalf("expected v0.2.1, got %s", tag)
	}

	// Case 4: No canonical tags must fail closed
	_, err = runWithTags([]string{})
	if err == nil {
		t.Fatal("expected failure when no canonical release tags exist")
	}
}

