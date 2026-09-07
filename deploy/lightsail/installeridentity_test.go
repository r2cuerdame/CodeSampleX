package lightsail

import (
	"os"
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
		`git -C $repo tag --points-at $revision --list 'v*'`,
		`$releaseTags.Count -ne 1`,
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
