package domain

import "testing"

func TestWantedTargetFromFrameworkUsesFixedPublicVocabulary(t *testing.T) {
	for _, tc := range []struct {
		framework string
		want      string
	}{
		{"unity@6000.0.24f1", "pkg:generic/engine/unity@6000.0.24f1"},
		{"unreal@5.6.1", "pkg:generic/engine/unreal@5.6.1"},
		{"android-sdk@35", "pkg:generic/sdk/android@35"},
		{"jdk@21.0.2", "pkg:generic/sdk/jdk@21.0.2"},
		{"git@2.51.0", "pkg:generic/cli/git@2.51.0"},
		{"npm@11.5.2", "pkg:generic/cli/npm@11.5.2"},
		{"pnpm@10.15.0", "pkg:generic/cli/pnpm@10.15.0"},
		{"maven@3.9.11", "pkg:generic/cli/maven@3.9.11"},
		{"gem@3.7.2", "pkg:generic/cli/gem@3.7.2"},
		{"powershell@7.5.2", "pkg:generic/cli/powershell@7.5.2"},
		{"ubuntu@24.04", "pkg:generic/os/ubuntu@24.04"},
		{"rhel@9.6", "pkg:generic/os/rhel@9.6"},
	} {
		p, ok := WantedTargetFromFramework(tc.framework)
		if !ok || p.String() != tc.want || !IsWantedTarget(p) {
			t.Errorf("WantedTargetFromFramework(%q) = (%s,%v), want %s", tc.framework, p.String(), ok, tc.want)
		}
	}
	if p, ok := PublicTargetFromDescriptor("gh@2.78.0"); !ok || p.String() != "pkg:generic/cli/gh@2.78.0" {
		t.Fatalf("PublicTargetFromDescriptor(gh) = (%s,%v)", p.String(), ok)
	}
	for _, value := range []string{
		"company-sdk@1.0.0", "unity@latest", "unity", "private-product@7",
	} {
		if p, ok := WantedTargetFromFramework(value); ok {
			t.Errorf("private or non-concrete target %q was accepted as %s", value, p.String())
		}
	}
}

func TestArbitraryGenericPURLIsNotAKnownWantedTarget(t *testing.T) {
	p, err := ParsePURL("pkg:generic/sdk/company-secret@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if IsWantedTarget(p) {
		t.Fatal("arbitrary generic coordinate crossed the fixed target allowlist")
	}
}
