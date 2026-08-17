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
	} {
		p, ok := WantedTargetFromFramework(tc.framework)
		if !ok || p.String() != tc.want || !IsWantedTarget(p) {
			t.Errorf("WantedTargetFromFramework(%q) = (%s,%v), want %s", tc.framework, p.String(), ok, tc.want)
		}
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
