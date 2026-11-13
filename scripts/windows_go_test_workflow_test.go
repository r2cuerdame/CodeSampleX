package main

import (
	"strings"
	"testing"
)

func TestWindowsGoTestWorkflowsSetPathsBeforeGo(t *testing.T) {
	for name, workflow := range map[string]string{"CI": ciWorkflow(t), "release": releaseWorkflow(t)} {
		t.Run(name, func(t *testing.T) {
			setup := strings.Index(workflow, "run: . ./scripts/windows-go-test-env.ps1 -ExportGitHubEnvironment")
			registry := strings.Index(workflow, "run: go test -timeout 3m")
			if setup < 0 || registry < 0 || setup >= registry {
				t.Fatal("Windows environment must be exported before Go builds its first test executable")
			}
		})
	}
}
