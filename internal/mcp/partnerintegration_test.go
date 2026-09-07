package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type partnerProfile struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Product       struct {
		PartnerPitch          string `json:"partnerPitch"`
		Role                  string `json:"role"`
		ReplacesHostAssistant bool   `json:"replacesHostAssistant"`
	} `json:"product"`
	Presentation struct {
		Placement          string `json:"placement"`
		DefaultEnabled     bool   `json:"defaultEnabled"`
		ShowProtocolToUser bool   `json:"showProtocolToUser"`
	} `json:"presentation"`
	Provisioning struct {
		Source            string `json:"source"`
		RegistryID        string `json:"registryId"`
		PackageType       string `json:"packageType"`
		HostOwnsLifecycle bool   `json:"hostOwnsLifecycle"`
	} `json:"provisioning"`
	Activation struct {
		RequiresModeChoice bool     `json:"requiresModeChoice"`
		Modes              []string `json:"modes"`
		ReadyAfter         []string `json:"readyAfter"`
		ModeSetup          struct {
			ExecutableSource string `json:"executableSource"`
			Arguments        struct {
				Community []string `json:"community"`
				LocalOnly []string `json:"local-only"`
			} `json:"arguments"`
		} `json:"modeSetup"`
	} `json:"activation"`
	Semantics struct {
		UnknownResult                          string `json:"unknownResult"`
		PreserveEnvironmentCoordinates         bool   `json:"preserveEnvironmentCoordinates"`
		KeepObservationAndVerificationSeparate bool   `json:"keepObservationAndVerificationSeparate"`
		HostRetainsReasoning                   bool   `json:"hostRetainsReasoning"`
	} `json:"semantics"`
	Capabilities struct {
		AutomaticRecall            []string `json:"automaticRecall"`
		OnDemandRead               []string `json:"onDemandRead"`
		RequiresHostActionApproval []string `json:"requiresHostActionApproval"`
		ExplicitWorkflowOnly       []string `json:"explicitWorkflowOnly"`
	} `json:"capabilities"`
}

func readPartnerProfile(t *testing.T) partnerProfile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(docsRepoRoot(t), "integrations", "partner-profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	var profile partnerProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("partner profile is not valid JSON: %v", err)
	}
	return profile
}

func TestPartnerProfileIsARecommendedProductNotAReplacement(t *testing.T) {
	profile := readPartnerProfile(t)
	if profile.SchemaVersion != 1 || profile.ID != "io.github.r2cuerdame/codesamplex" {
		t.Fatalf("identity = v%d %q", profile.SchemaVersion, profile.ID)
	}
	if profile.Product.PartnerPitch != "Your coding assistant keeps reasoning; CSX adds real-world execution memory." {
		t.Errorf("partner pitch drifted: %q", profile.Product.PartnerPitch)
	}
	if profile.Product.Role != "coding-assist-evidence-layer" || profile.Product.ReplacesHostAssistant {
		t.Errorf("product role implies replacement: %+v", profile.Product)
	}
	if profile.Presentation.Placement != "recommended-toggle" || profile.Presentation.DefaultEnabled || profile.Presentation.ShowProtocolToUser {
		t.Errorf("presentation no longer describes one consent-safe product toggle: %+v", profile.Presentation)
	}
}

func TestPartnerProfileUsesTheManagedRegistryPackageAndKeepsManualGates(t *testing.T) {
	profile := readPartnerProfile(t)
	if profile.Provisioning.Source != "official-mcp-registry" ||
		profile.Provisioning.RegistryID != profile.ID ||
		profile.Provisioning.PackageType != "mcpb" ||
		!profile.Provisioning.HostOwnsLifecycle {
		t.Errorf("provisioning bypasses the managed package path: %+v", profile.Provisioning)
	}
	if !profile.Activation.RequiresModeChoice || strings.Join(profile.Activation.Modes, ",") != "community,local-only" {
		t.Errorf("activation no longer preserves the privacy choice: %+v", profile.Activation)
	}
	if strings.Join(profile.Activation.ReadyAfter, ",") != "initialize,notifications/initialized" {
		t.Errorf("readiness is not tied to the protocol lifecycle: %v", profile.Activation.ReadyAfter)
	}
	if profile.Activation.ModeSetup.ExecutableSource != "managed-package" {
		t.Errorf("mode setup does not use the managed package: %+v", profile.Activation.ModeSetup)
	}
	if got := strings.Join(profile.Activation.ModeSetup.Arguments.Community, " "); got != "init --community --yes --no-agents --no-daemon" {
		t.Errorf("community setup = %q", got)
	}
	if got := strings.Join(profile.Activation.ModeSetup.Arguments.LocalOnly, " "); got != "init --local-only --yes --no-agents --no-daemon" {
		t.Errorf("local-only setup = %q", got)
	}
	if profile.Semantics.UnknownResult != "NO_SAFE_MATCH" ||
		!profile.Semantics.PreserveEnvironmentCoordinates ||
		!profile.Semantics.KeepObservationAndVerificationSeparate ||
		!profile.Semantics.HostRetainsReasoning {
		t.Errorf("partner adapter weakens CSX evidence semantics: %+v", profile.Semantics)
	}
}

func TestPartnerProfileClassifiesEveryPublicToolExactlyOnce(t *testing.T) {
	profile := readPartnerProfile(t)
	classified := append([]string{}, profile.Capabilities.AutomaticRecall...)
	classified = append(classified, profile.Capabilities.OnDemandRead...)
	classified = append(classified, profile.Capabilities.RequiresHostActionApproval...)
	classified = append(classified, profile.Capabilities.ExplicitWorkflowOnly...)
	sort.Strings(classified)

	want := make([]string, 0, len(toolDefs()))
	for _, tool := range toolDefs() {
		want = append(want, tool.Name)
	}
	sort.Strings(want)
	if strings.Join(classified, "\n") != strings.Join(want, "\n") {
		t.Fatalf("classified tools = %v\npublic tools = %v", classified, want)
	}
	for i := 1; i < len(classified); i++ {
		if classified[i] == classified[i-1] {
			t.Fatalf("tool %q is classified more than once", classified[i])
		}
	}
}

func TestPartnerProfileSchemaAndGuideAreValidRepositoryDocuments(t *testing.T) {
	root := docsRepoRoot(t)
	for _, rel := range []string{
		filepath.Join("schemas", "v1", "partner-integration.json"),
		filepath.Join("integrations", "partner-profile.json"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s is not valid JSON: %v", rel, err)
		}
	}
	guide, err := os.ReadFile(filepath.Join(root, "docs", "partner-integration.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"recommended toggle", "Community", "Local only", "NO_SAFE_MATCH", "run_observed_command"} {
		if !strings.Contains(string(guide), required) {
			t.Errorf("partner guide no longer names %q", required)
		}
	}
}
