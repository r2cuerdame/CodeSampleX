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

// The OS the miss happened on is the half of the wanted signal the platform
// routing runs on. It survives the upload gate — and only when it is one of
// the platforms verification can target: the gate's whole job is that a
// hand-edited queue row cannot smuggle a free-text environment string (a
// fingerprint) through the daemon, and the server 400s the WHOLE batch on an
// unknown os, so a poisoned row must be cleaned here rather than block every
// report travelling with it.
func TestWantedUploadKeepsTheReportedOS(t *testing.T) {
	payload := `{"schemaVersion":1,"epoch":"2026-08-18","anonId":"0123456789abcdef",` +
		`"packages":["pkg:generic/cli/maven@3.9.11"],"os":"windows"}`
	clean, err := PrepareWantedForUpload(context.Background(), payload,
		func(context.Context, domain.PURL) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(clean), `"os":"windows"`) {
		t.Fatalf("clean report dropped the reported OS: %s", clean)
	}

	poisoned := `{"schemaVersion":1,"epoch":"2026-08-18","anonId":"0123456789abcdef",` +
		`"packages":["pkg:generic/cli/maven@3.9.11"],"os":"Gentoo 2.15 (custom kernel)"}`
	clean, err = PrepareWantedForUpload(context.Background(), poisoned,
		func(context.Context, domain.PURL) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(clean), "Gentoo") {
		t.Fatalf("a free-text environment string crossed the upload boundary: %s", clean)
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
