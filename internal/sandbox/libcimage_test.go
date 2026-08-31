package sandbox

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// A manifest that declares glibc must not be verified on musl.
//
// Measured on production 2026-09-01. Ten npm samples declared
// environment.libc = "glibc" and every one was run on node:22-alpine, which
// is musl. Six failed inside two minutes with:
//
//	Error loading shared library ld-linux-x86-64.so.2: No such file or
//	directory · code: 'ERR_DLOPEN_FAILED'
//
// A prebuilt native .node binary is built against one libc and does not load
// on the other, which is what this package's own EnvironmentFingerprint.Libc
// comment says the dimension exists to capture. The receipt recorded
// contract=FAIL: a verdict on the sample, for a mismatch between what its
// author declared and what the verifier provided.
//
// python:3.12-slim was moved off Alpine for exactly this reason and the note
// is still in the registry. npm was not moved with it.
func TestADeclaredLibcSelectsAMatchingImage(t *testing.T) {
	for _, tc := range []struct {
		name string
		libc string
		want string
	}{
		{"glibc gets the Debian line", "glibc", "node:22@"},
		{"musl keeps Alpine", "musl", "node:22-alpine@"},
		{"an undeclared libc keeps the historical default", "", "node:22-alpine@"},
	} {
		got, err := imageForManifestLinux(domain.SampleManifest{
			Environment: domain.EnvironmentFingerprint{
				SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
				Runtime: "node", Libc: tc.libc,
			},
		})
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s: image = %q, want one pinned from %s", tc.name, got, tc.want)
		}
	}
}

// A libc this network has no image for is refused, never substituted.
//
// The alternative is a receipt that says a sample was verified in an
// environment it was not verified in, which is the one claim this project
// cannot make. It is the same rule the Python runtime lines already follow:
// refuse rather than silently produce a receipt for a different interpreter.
func TestALibcWithNoImageIsRefused(t *testing.T) {
	_, err := imageForManifestLinux(domain.SampleManifest{
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "x64",
			Runtime: "bun", Libc: "glibc",
		},
	})
	if err == nil {
		t.Fatal("a glibc bun manifest was accepted; no glibc bun image is published")
	}
	if !strings.Contains(err.Error(), "libc") {
		t.Errorf("the refusal does not name the reason: %v", err)
	}
}

// Every LINUX image declares which libc it provides, so the guard above cannot
// be defeated by an entry that simply does not say.
//
// Windows images declare none, and that is correct rather than an omission:
// there is no libc there, and inventing one would make the guard compare a
// manifest's "glibc" against a word that means nothing on that platform.
func TestEveryLinuxVerifierImageDeclaresItsLibc(t *testing.T) {
	for alias, img := range verifierImages {
		if strings.Contains(alias, "windows") {
			if img.libc != "" {
				t.Errorf("%s claims libc %q on Windows", alias, img.libc)
			}
			continue
		}
		if strings.TrimSpace(img.libc) == "" {
			t.Errorf("%s does not say which libc it provides", alias)
		}
	}
}
