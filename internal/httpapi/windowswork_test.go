package httpapi

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func req(capability domain.SandboxCapability, oses ...string) authoringWorkRequest {
	return authoringWorkRequest{SchemaVersion: 1, SandboxCapability: capability, VerifierOS: oses}
}

// A Windows daemon can only run the ecosystems that publish a Windows base
// image. Handing it npm work is handing it a job that fails before its first
// stage — Node publishes no such image, which is why sandbox.SupportsWindows
// answers from the images rather than a list beside them.
func TestWindowsVerifierOnlyGetsEcosystemsItCanRun(t *testing.T) {
	windows := req(domain.CapContainerRun, "windows")
	for _, eco := range []string{"golang", "pypi"} {
		candidate := serverstore.WantedRow{Ecosystem: eco, Version: "1.0.0", Kind: "EXPANSION", TargetOS: "windows"}
		if !authoringCandidateEligible(candidate, windows) {
			t.Errorf("%s is runnable on windows but was refused", eco)
		}
	}
	for _, eco := range []string{"npm", "cargo", "gem", "maven"} {
		candidate := serverstore.WantedRow{Ecosystem: eco, Version: "1.0.0", Kind: "EXPANSION", TargetOS: "windows"}
		if authoringCandidateEligible(candidate, windows) {
			t.Errorf("%s has no Windows image but was offered to a Windows verifier", eco)
		}
	}
}

// WANTED is somebody's explicit ask, but it still has to be runnable: the
// old code returned true for every WANTED regardless of environment, which on
// a Windows worker means npm work it cannot start.
func TestWantedIsStillGatedByWhatTheVerifierCanRun(t *testing.T) {
	windows := req(domain.CapContainerRun, "windows")
	if authoringCandidateEligible(serverstore.WantedRow{Ecosystem: "npm", Version: "1.0.0", Kind: "WANTED"}, windows) {
		t.Error("a Windows verifier was offered npm WANTED work")
	}
	if !authoringCandidateEligible(serverstore.WantedRow{Ecosystem: "golang", Version: "1.0.0", Kind: "WANTED"}, windows) {
		t.Error("a Windows verifier was refused golang WANTED work")
	}
	// Linux keeps every supported ecosystem.
	linux := req(domain.CapContainerRun, "linux")
	if !authoringCandidateEligible(serverstore.WantedRow{Ecosystem: "npm", Version: "1.0.0", Kind: "WANTED"}, linux) {
		t.Error("a Linux verifier lost npm WANTED work")
	}
}

// A worker serving Windows containers must be able to say so. Refusing the
// declaration was what kept every receipt in the network stamped linux.
func TestWorkRequestAcceptsAWindowsVerifier(t *testing.T) {
	for _, os := range []string{"windows", "linux", "WINDOWS", " linux "} {
		if _, err := normalizeVerifierOS([]string{os}); err != nil {
			t.Errorf("%q was refused: %v", os, err)
		}
	}
	for _, os := range []string{"darwin", "", "plan9"} {
		if _, err := normalizeVerifierOS([]string{os}); err == nil {
			t.Errorf("%q was accepted", os)
		}
	}
	got, err := normalizeVerifierOS([]string{"WINDOWS", " linux "})
	if err != nil || len(got) != 2 || got[0] != "windows" || got[1] != "linux" {
		t.Errorf("normalize = %v err=%v", got, err)
	}
}
