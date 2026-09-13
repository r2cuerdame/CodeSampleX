package domain

import "testing"

func TestCLIRecognitionAndPublicVocabularyCannotDrift(t *testing.T) {
	tools := map[string]bool{}
	for tool := range buildToolEcosystems {
		tools[tool] = true
	}
	for tool := range multiWordSubcommands {
		tools[tool] = true
	}
	for tool, coordinate := range wantedTargetNames {
		if len(coordinate) > 4 && coordinate[:4] == "cli/" {
			tools[tool] = true
		}
	}
	for tool := range tools {
		if !IsRecognizedCLITool(tool) {
			t.Errorf("existing CLI %s stopped being recognized", tool)
			continue
		}
		p, ok := (CLIExperienceCoordinate{Tool: tool, ToolVersion: "1.0.0"}).PURL()
		if !ok || !IsWantedTarget(p) {
			t.Errorf("recognized CLI %s cannot cross the public boundary: %v", tool, p)
		}
	}
	for _, tool := range []string{"company-secret", "private-cli", "unity"} {
		if IsRecognizedCLITool(tool) {
			t.Errorf("non-CLI or private name %q admitted", tool)
		}
	}
}
