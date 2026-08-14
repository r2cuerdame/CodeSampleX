package domain

import "testing"

// A caller reports what it could detect. Whatever it had to leave unknown
// must become absence, because the grader compares a dimension only when
// both sides declare it. Compared as a literal, "unknown" made an
// undetected package manager look like a different one: every otherwise
// exact match was downgraded to ADAPTATION_REQUIRED and the caller was
// told to "use unknown equivalents of lockfile commands".
func TestNormalizeErasesPlaceholderValues(t *testing.T) {
	got := EnvironmentFingerprint{
		SchemaVersion:  1,
		Ecosystem:      "pypi",
		OS:             "linux",
		Arch:           "unknown",
		Runtime:        "python",
		RuntimeVersion: "N/A",
		Language:       " Unspecified ",
		PackageManager: "unknown",
		ModuleSystem:   "?",
		Libc:           "-",
	}.Normalize()

	for name, v := range map[string]string{
		"Arch": got.Arch, "RuntimeVersion": got.RuntimeVersion,
		"Language": got.Language, "PackageManager": got.PackageManager,
		"ModuleSystem": got.ModuleSystem, "Libc": got.Libc,
	} {
		if v != "" {
			t.Errorf("%s = %q, want empty", name, v)
		}
	}
	if got.OS != "linux" || got.Runtime != "python" {
		t.Errorf("real values were erased: %+v", got)
	}
	// The runtime still implies its context; erasing placeholders must not
	// interfere with the derivation Normalize already did.
	if got.Ecosystem != "pypi" {
		t.Errorf("ecosystem = %q", got.Ecosystem)
	}
}

// "none" is an answer, not a non-answer: bare metal, no container, and a
// statically linked binary all report it, and erasing it would discard a
// fact rather than a placeholder.
func TestNormalizeKeepsNoneWhereItIsAnAnswer(t *testing.T) {
	got := EnvironmentFingerprint{
		SchemaVersion:    1,
		Virtualization:   "none",
		ContainerRuntime: "none",
		Libc:             "none",
	}.Normalize()

	if got.Virtualization != "none" || got.ContainerRuntime != "none" || got.Libc != "none" {
		t.Errorf("none was erased: %+v", got)
	}
}
