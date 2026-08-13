package samples

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func specInputs() ScanInputs {
	return ScanInputs{
		Packages: []string{"pkg:npm/zod@3.23.8", "pkg:npm/axios@1.12.0", "pkg:npm/axios@1.12.0"},
		Symbols:  []string{"axios.post", "z.object"},
		Goal:     "POST JSON with a validated body",
		Kind:     "HOW",
		Constraints: map[string]string{
			"http": "must send application/json",
		},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion:  1,
			Ecosystem:      "npm",
			OS:             "windows",
			Arch:           "amd64",
			Runtime:        "node",
			RuntimeVersion: "22.18.0",
			ModuleSystem:   "esm",
			PackageManager: "pnpm",
		},
		PassedStages:    []string{"PROJECT_TYPECHECK", "PROJECT_COMPILE"},
		UserDescription: "we needed retry-safe posting",
	}
}

func TestSpecBuildSpec(t *testing.T) {
	spec := BuildSpec(specInputs())
	if spec.SchemaVersion != 1 {
		t.Fatalf("schemaVersion %d", spec.SchemaVersion)
	}
	if spec.Goal != "POST JSON with a validated body" || spec.Kind != "HOW" {
		t.Fatalf("goal/kind not carried: %+v", spec)
	}
	// Packages deduped + sorted.
	want := []string{"pkg:npm/axios@1.12.0", "pkg:npm/zod@3.23.8"}
	if len(spec.Packages) != 2 || spec.Packages[0] != want[0] || spec.Packages[1] != want[1] {
		t.Fatalf("packages %v, want %v", spec.Packages, want)
	}
	if spec.Constraints["executionContext"] != "node" {
		t.Fatalf("executionContext constraint missing: %v", spec.Constraints)
	}
	if spec.Constraints["http"] != "must send application/json" {
		t.Fatalf("user constraint lost: %v", spec.Constraints)
	}
	if spec.RuntimeConditions["runtime"] != "node@22.18.0" {
		t.Fatalf("runtime condition missing: %v", spec.RuntimeConditions)
	}
	if spec.RuntimeConditions["moduleSystem"] != "esm" {
		t.Fatalf("moduleSystem condition missing: %v", spec.RuntimeConditions)
	}
	if len(spec.PassedStages) != 2 {
		t.Fatalf("passed stages lost: %v", spec.PassedStages)
	}
	if spec.UserDescription == "" {
		t.Fatal("user description lost")
	}
}

// The wire form of a SanitizedSpec must contain only whitelisted keys —
// nothing project-identifying can even be represented (goal.md §9.2).
func TestSpecJSONHasOnlyWhitelistedKeys(t *testing.T) {
	raw, err := json.Marshal(BuildSpec(specInputs()))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"schemaVersion": true, "goal": true, "kind": true, "packages": true,
		"symbols": true, "constraints": true, "runtimeConditions": true,
		"passedStages": true, "userDescription": true,
	}
	for k := range m {
		if !allowed[k] {
			t.Errorf("unexpected key %q in sanitized spec JSON", k)
		}
	}
}

func TestSpecPromptText(t *testing.T) {
	spec := BuildSpec(specInputs())
	p := spec.PromptText()
	for _, want := range []string{
		"pkg:npm/axios@1.12.0",
		"POST JSON with a validated body",
		"axios.post",
		"executionContext",
		"contract",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, p)
		}
	}
	lower := strings.ToLower(p)
	for _, forbidden := range []string{"copy", "existing project"} {
		if !strings.Contains(lower, forbidden) {
			t.Errorf("prompt should warn about %q (clean-room rule)", forbidden)
		}
	}
	// Determinism: map-backed sections must render identically every time.
	if p != spec.PromptText() {
		t.Fatal("PromptText not deterministic")
	}
}
