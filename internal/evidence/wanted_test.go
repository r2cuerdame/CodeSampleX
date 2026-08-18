package evidence

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestKnownPublicTargetUploadsWithoutRegistryLookup(t *testing.T) {
	payload := `{"schemaVersion":1,"epoch":"2026-08-18","anonId":"0123456789abcdef",` +
		`"packages":["pkg:generic/engine/unity@6000.0.24f1"],` +
		`"symbols":["AssetDatabase.Refresh"]}`
	lookups := 0
	clean, err := PrepareWantedForUpload(context.Background(), payload,
		func(context.Context, domain.PURL) bool {
			lookups++
			return false
		})
	if err != nil {
		t.Fatal(err)
	}
	if lookups != 0 {
		t.Fatalf("known engine target made %d registry lookups", lookups)
	}
	if !strings.Contains(string(clean), "pkg:generic/engine/unity@6000.0.24f1") {
		t.Fatalf("clean report dropped known target: %s", clean)
	}
}

func TestKnownCLITargetUploadsWithoutRegistryLookup(t *testing.T) {
	payload := `{"schemaVersion":1,"epoch":"2026-08-18","anonId":"0123456789abcdef",` +
		`"packages":["pkg:generic/cli/maven@3.9.11"],"symbols":["mvn dependency:go-offline"]}`
	lookups := 0
	clean, err := PrepareWantedForUpload(context.Background(), payload,
		func(context.Context, domain.PURL) bool {
			lookups++
			return false
		})
	if err != nil {
		t.Fatal(err)
	}
	if lookups != 0 || !strings.Contains(string(clean), "pkg:generic/cli/maven@3.9.11") {
		t.Fatalf("known CLI target was not preserved without registry lookup: lookups=%d payload=%s", lookups, clean)
	}
}

func TestArbitraryGenericTargetCannotLeaveMachine(t *testing.T) {
	payload := `{"schemaVersion":1,"epoch":"2026-08-18","anonId":"0123456789abcdef",` +
		`"packages":["pkg:generic/sdk/company-secret@1.0.0"]}`
	if _, err := PrepareWantedForUpload(context.Background(), payload,
		func(context.Context, domain.PURL) bool { return true }); err == nil {
		t.Fatal("arbitrary generic target crossed the upload boundary")
	}
}
